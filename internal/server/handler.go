// Package server 提供本地 HTTP 服务和路由。
package server

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"clodesen2api/internal/admission"
	"clodesen2api/internal/apikey"
	"clodesen2api/internal/modelpolicy"
	"clodesen2api/internal/openai"
	"clodesen2api/internal/upstream"
	"clodesen2api/internal/usage"
)

//go:embed web
var adminAssets embed.FS

var requestSequence atomic.Uint64

var forwardedPaths = [...]string{
	openai.ModelsPath,
	openai.ChatCompletionsPath,
	openai.EditsPath,
	openai.GenerationsPath,
}

// NewHandler 创建 HTTP 路由。
// 只有 forwardedPaths 中的精确路径会调用上游，其他路径由 ServeMux 返回 404。
func NewHandler(up *upstream.Upstream, adminHandlers ...http.Handler) http.Handler {
	return NewHandlerWithUsage(up, usage.New(), adminHandlers...)
}

func NewHandlerWithUsage(up *upstream.Upstream, stats *usage.Collector, adminHandlers ...http.Handler) http.Handler {
	return NewHandlerWithAPIKey(up, stats, nil, adminHandlers...)
}

// NewHandlerWithAPIKey enables client API-key admission for the forwarded
// OpenAI-compatible routes. A nil authenticator preserves the library's
// unauthenticated embedding behavior; the production entrypoint always
// supplies one.
func NewHandlerWithAPIKey(up *upstream.Upstream, stats *usage.Collector, auth *apikey.Authenticator, adminHandlers ...http.Handler) http.Handler {
	return NewHandlerWithPolicies(up, stats, auth, nil, adminHandlers...)
}

func NewHandlerWithPolicies(up *upstream.Upstream, stats *usage.Collector, auth *apikey.Authenticator, policies *modelpolicy.Store, adminHandlers ...http.Handler) http.Handler {
	return NewHandlerWithAdmission(up, stats, auth, policies, nil, adminHandlers...)
}

func NewHandlerWithAdmission(up *upstream.Upstream, stats *usage.Collector, auth *apikey.Authenticator, policies *modelpolicy.Store, capacity *admission.Store, adminHandlers ...http.Handler) http.Handler {
	if stats == nil {
		stats = usage.New()
	}
	mux := http.NewServeMux()

	// 健康检查仅由本地处理，不会转发到上游。
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("ok"))
	})
	mux.HandleFunc("/readyz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		var err error
		if len(adminHandlers) > 0 {
			if provider, ok := adminHandlers[0].(interface{ Ready(context.Context) error }); ok {
				err = provider.Ready(ctx)
			}
		}
		if err != nil || up.RoutingHealth().Available == 0 {
			http.Error(w, "not ready", http.StatusServiceUnavailable)
			return
		}
		_, _ = w.Write([]byte("ready"))
	})
	staticFiles, _ := fs.Sub(adminAssets, "web")
	mux.HandleFunc("/admin", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/admin/", http.StatusMovedPermanently)
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" && (r.Method == http.MethodGet || r.Method == http.MethodHead) {
			http.Redirect(w, r, "/admin/", http.StatusFound)
			return
		}
		http.NotFound(w, r)
	})
	mux.Handle("/admin/", adminHeaders(http.StripPrefix("/admin/", http.FileServer(http.FS(staticFiles)))))
	if len(adminHandlers) > 0 && adminHandlers[0] != nil {
		mux.Handle("/admin/api/", adminHeaders(adminHandlers[0]))
	}

	forward := func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		clientIP := requestRemoteIP(r)
		requestID := fmt.Sprintf("req_%x_%x", start.UnixNano(), requestSequence.Add(1))
		w.Header().Set("X-Request-ID", requestID)
		var principal apikey.Principal
		if auth != nil {
			remoteIP := requestRemoteIP(r)
			clientIP = auth.ClientIP(remoteIP, r.Header.Get("X-Forwarded-For"))
			var authErr *apikey.AuthError
			principal, authErr = auth.PreAuthenticate(remoteIP, r.Header.Get("X-Forwarded-For"), r.Header.Get("Authorization"), r.Header.Get("X-API-Key"))
			if authErr != nil {
				recordRejectedAuth(stats, r.URL.Path, "", principal, clientIP, requestID, start, authErr)
				writeAuthError(w, authErr)
				log.Printf("[auth] rejected %s %s: %s", r.Method, r.URL.Path, authErr.Code)
				return
			}
			r = r.WithContext(apikey.WithPrincipal(r.Context(), principal))
		}
		stats.RecordRequest(usage.RequestRecord{CreatedAt: start, RequestID: requestID, Stage: "started", Endpoint: r.URL.Path, APIKeyID: principal.ID, APIKeyName: principal.Name, ClientIP: clientIP, AuthResult: "authorized", Type: "unknown"})
		defer func() {
			if recovered := recover(); recovered != nil {
				stats.RecordRequest(usage.RequestRecord{CreatedAt: start, RequestID: requestID, Endpoint: r.URL.Path, APIKeyID: principal.ID, APIKeyName: principal.Name, ClientIP: clientIP, AuthResult: "authorized", Type: "unknown", Status: 500, Success: false, Error: "internal_panic", Stage: "interrupted"})
				panic(recovered)
			}
		}()
		var lease *admission.Lease
		denyCapacity := func(err error) {
			failure := &apikey.AuthError{Status: 503, Code: "admission_unavailable", Message: "inference admission is unavailable", RetryAfter: 3}
			var rejected *admission.Error
			var authFailure *apikey.AuthError
			if errors.As(err, &authFailure) {
				failure = authFailure
			}
			if errors.As(err, &rejected) {
				failure.Status = rejected.Status
				failure.Code = rejected.Code
				failure.Message = rejected.Code
				failure.RetryAfter = rejected.RetryAfter
			}
			recordRejectedAuth(stats, r.URL.Path, "", principal, clientIP, requestID, start, failure)
			writeAuthError(w, failure)
		}
		if capacity != nil && r.URL.Path != openai.ModelsPath && r.Method == http.MethodPost {
			var err error
			lease, err = capacity.Acquire(r.Context(), principal.ID, principal.Limits, openai.BodyBudget(r.URL.Path, r.ContentLength))
			if err != nil {
				denyCapacity(err)
				return
			}
			defer func() {
				if err := lease.Release(); err != nil {
					log.Printf("[admission] release failed; lease will expire")
				}
			}()
			ctx, cancel := context.WithTimeout(r.Context(), time.Duration(lease.Settings.RequestTimeoutSeconds+lease.Settings.BodyTimeoutSeconds)*time.Second)
			defer cancel()
			ctx, stopLease, err := lease.Bind(ctx)
			if err != nil {
				denyCapacity(err)
				return
			}
			defer stopLease()
			r = r.WithContext(admission.WithLimits(ctx, lease.Settings))
			_ = http.NewResponseController(w).SetReadDeadline(time.Now().Add(time.Duration(lease.Settings.BodyTimeoutSeconds) * time.Second))
		}
		request, err := openai.BuildRequest(w, r)
		if lease != nil {
			_ = http.NewResponseController(w).SetReadDeadline(time.Time{})
			_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(time.Duration(lease.Settings.RequestTimeoutSeconds) * time.Second))
		}
		if err != nil {
			recordRejectedRequest(stats, r.URL.Path, principal, clientIP, requestID, start, err)
			writeRequestError(w, err)
			log.Printf("[request] rejected %s %s: %v", r.Method, r.URL.Path, err)
			return
		}
		publicModel, sourceModel := request.Model, request.Model
		var meter admission.Meter
		var policyRevision int64
		if policies != nil && request.Path != openai.ModelsPath {
			snapshot, err := policies.Snapshot(r.Context())
			if err != nil {
				authErr := &apikey.AuthError{Status: 503, Code: "model_policy_unavailable", Message: "model policy store is unavailable", RetryAfter: 3}
				recordRejectedAuth(stats, r.URL.Path, publicModel, principal, clientIP, requestID, start, authErr)
				writeAuthError(w, authErr)
				return
			}
			rule, err := snapshot.Resolve(publicModel, request.Path, request.Stream)
			if err != nil {
				authErr := &apikey.AuthError{Status: 403, Code: "model_policy_denied", Message: err.Error()}
				recordRejectedAuth(stats, r.URL.Path, publicModel, principal, clientIP, requestID, start, authErr)
				writeAuthError(w, authErr)
				return
			}
			sourceModel, policyRevision = rule.SourceID, snapshot.Revision
			meter = rule.Metering
			request, err = request.WithModel(rule.UpstreamID, publicModel)
			if err != nil {
				recordRejectedRequest(stats, r.URL.Path, principal, clientIP, requestID, start, err)
				writeRequestError(w, err)
				return
			}
		}
		if auth != nil {
			var authErr *apikey.AuthError
			if request.Path == openai.ModelsPath {
				authErr = auth.Validate(principal)
			} else {
				authErr = auth.CheckModel(principal, sourceModel)
			}
			if authErr != nil {
				recordRejectedAuth(stats, r.URL.Path, publicModel, principal, clientIP, requestID, start, authErr)
				writeAuthError(w, authErr)
				log.Printf("[auth] rejected %s %s: %s", r.Method, r.URL.Path, authErr.Code)
				return
			}
		}
		if lease != nil {
			var check admission.Check
			if auth != nil {
				check = auth.AdmissionCheck(principal, sourceModel)
			}
			if err := lease.AdmitMetered(r.Context(), request.Model, principal.Limits, principal.RPM, check, meter, requestID); err != nil {
				denyCapacity(err)
				return
			}
			if lease.Metered() {
				limits := lease.Settings
				limits.MaxAttempts = 1
				var actual admission.Amount
				r = r.WithContext(admission.WithMeter(admission.WithLimits(r.Context(), limits), func(value admission.Amount) { actual = value }))
				defer func() {
					ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
					defer cancel()
					if err := lease.Settle(ctx, actual); err != nil {
						log.Printf("[admission] settlement failed; reservation retained")
					}
				}()
			}
		}
		if auth != nil && request.Path != openai.ModelsPath && lease == nil {
			if authErr := auth.Authorize(principal, sourceModel); authErr != nil {
				recordRejectedAuth(stats, r.URL.Path, publicModel, principal, clientIP, requestID, start, authErr)
				writeAuthError(w, authErr)
				return
			}
		}

		response := &trackingResponseWriter{ResponseWriter: w, requestID: requestID}
		var forwardErr error
		var wrote bool
		stats.RecordRequest(usage.RequestRecord{CreatedAt: start, RequestID: requestID, Stage: "started", Endpoint: request.Path, Model: publicModel, SourceModel: sourceModel, UpstreamModel: request.Model, PolicyRevision: policyRevision, APIKeyID: principal.ID, APIKeyName: principal.Name, ClientIP: clientIP, AuthResult: "authorized", Type: requestType(request)})
		{
			defer func() {
				recovered := recover()
				status, stage := response.status, "completed"
				if recovered != nil {
					status, stage = http.StatusInternalServerError, "interrupted"
					forwardErr = errors.New("internal panic")
					defer func() { panic(recovered) }()
				}
				duration := time.Since(start)
				if request.Path != openai.ModelsPath {
					stats.Record(publicModel, status, duration, forwardErr)
				}
				recordedError := requestErrorText(forwardErr)
				if recovered != nil {
					recordedError = "internal_panic"
				}
				authResult, authCode := "authorized", ""
				var authFailure *apikey.AuthError
				if errors.As(forwardErr, &authFailure) {
					authResult, authCode = "denied", authFailure.Code
				}
				if recordedError == "" && len(response.errorBody) > 0 {
					recordedError = safeUpstreamError(response.status, response.errorBody)
				}
				var firstTokenMS *int64
				if requestType(request) != "image" && !response.firstByteAt.IsZero() {
					value := response.firstByteAt.Sub(start).Milliseconds()
					firstTokenMS = &value
				}
				stats.RecordRequest(usage.RequestRecord{
					CreatedAt: start, Model: publicModel, SourceModel: sourceModel, UpstreamModel: request.Model, PolicyRevision: policyRevision, DurationMS: duration.Milliseconds(),
					FirstTokenMS: firstTokenMS, ThinkingLevel: request.ThinkingLevel, Prompt: request.Prompt,
					AccountID: response.accountID, Account: response.account,
					APIKeyID: principal.ID, APIKeyName: principal.Name, ClientIP: clientIP,
					AuthResult: authResult, AuthErrorCode: authCode, RequestID: requestID,
					Endpoint: request.Path, Type: requestType(request), Status: status, Stage: stage,
					Success: forwardErr == nil && status >= http.StatusOK && status < http.StatusMultipleChoices,
					Error:   recordedError, Parameters: request.Parameters, Attempts: response.attempts,
				})
			}()
		}
		if policies != nil && request.Path == openai.ModelsPath {
			forwardErr, wrote = forwardPublicModels(r.Context(), response, up, policies, auth, principal)
		} else {
			forwardErr, wrote = up.Forward(r.Context(), response, request)
		}
		cost := time.Since(start)
		if forwardErr != nil {
			// 尽量不覆盖已经写出的上游响应
			if errors.Is(forwardErr, context.Canceled) {
				log.Printf("[forward] client canceled %s %s cost=%s", r.Method, r.URL.Path, cost)
				return
			}
			log.Printf("[forward] error %s %s cost=%s: %s", r.Method, r.URL.Path, cost, requestErrorText(forwardErr))
			// 仅当响应还没写时返回 502
			if !wrote {
				response.Header().Set("Content-Type", "application/json")
				var unavailable *upstream.NoAccountAvailableError
				if errors.As(forwardErr, &unavailable) {
					retryAfter := int((unavailable.RetryAfter + time.Second - 1) / time.Second)
					if retryAfter < 1 {
						retryAfter = 1
					}
					response.Header().Set("Retry-After", strconv.Itoa(retryAfter))
					response.WriteHeader(http.StatusServiceUnavailable)
					_ = json.NewEncoder(response).Encode(map[string]string{
						"error": "all upstream accounts are temporarily unavailable",
					})
					return
				}
				status := http.StatusBadGateway
				if errors.Is(forwardErr, context.DeadlineExceeded) {
					status = http.StatusGatewayTimeout
				}
				response.WriteHeader(status)
				_ = json.NewEncoder(response).Encode(map[string]string{
					"error": requestErrorText(forwardErr),
				})
			}
			return
		}
		log.Printf("[forward] %s %s cost=%s", r.Method, r.URL.Path, cost)
	}

	for _, path := range forwardedPaths {
		mux.HandleFunc(path, forward)
	}

	return mux
}

type trackingResponseWriter struct {
	http.ResponseWriter
	status      int
	firstByteAt time.Time
	accountID   string
	account     string
	errorBody   []byte
	requestID   string
	attempts    []usage.Attempt
}

func (w *trackingResponseWriter) RecordUpstreamAttempt(accountID, identifier, model string, status int, duration time.Duration, wrote bool, errorCode string) {
	if len(w.attempts) >= 32 {
		return
	}
	w.attempts = append(w.attempts, usage.Attempt{ID: fmt.Sprintf("%s.%d", w.requestID, len(w.attempts)+1), AccountID: accountID, Account: identifier, Model: model, Status: status, DurationMS: duration.Milliseconds(), Wrote: wrote, ErrorCode: errorCode})
}

func (w *trackingResponseWriter) SetUpstreamAccount(accountID, identifier string) {
	w.accountID = accountID
	w.account = identifier
}

func (w *trackingResponseWriter) WriteHeader(status int) {
	if w.status == 0 {
		w.status = status
	}
	w.ResponseWriter.WriteHeader(status)
}

func (w *trackingResponseWriter) Write(body []byte) (int, error) {
	if w.status == 0 {
		w.WriteHeader(http.StatusOK)
	}
	if len(body) > 0 && w.firstByteAt.IsZero() {
		w.firstByteAt = time.Now()
	}
	if w.status >= http.StatusBadRequest && len(w.errorBody) < 512 {
		remaining := 512 - len(w.errorBody)
		captured := body
		if len(body) > remaining {
			captured = body[:remaining]
		}
		w.errorBody = append(w.errorBody, captured...)
	}
	return w.ResponseWriter.Write(body)
}

func (w *trackingResponseWriter) Flush() {
	if w.status == 0 {
		w.WriteHeader(http.StatusOK)
	}
	if flusher, ok := w.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}

func requestType(request openai.Request) string {
	if request.Path == openai.GenerationsPath || request.Path == openai.EditsPath {
		return "image"
	}
	if request.Stream {
		return "stream"
	}
	return "non_stream"
}

func requestErrorText(err error) string {
	if err == nil {
		return ""
	}
	if errors.Is(err, context.Canceled) {
		return "client_cancelled"
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "upstream_timeout"
	}
	if requestErr, ok := openai.AsRequestError(err); ok {
		return requestErr.Message
	}
	var unavailable *upstream.NoAccountAvailableError
	if errors.As(err, &unavailable) {
		return "upstream_unavailable"
	}
	var network net.Error
	if errors.As(err, &network) && network.Timeout() {
		return "upstream_timeout"
	}
	return "upstream_request_failed"
}

func safeUpstreamError(status int, body []byte) string {
	var payload struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	_ = json.Unmarshal(body, &payload)
	allowed := map[string]bool{"rate_limit_exceeded": true, "insufficient_quota": true, "insufficient_credits": true, "invalid_api_key": true, "model_not_found": true, "context_length_exceeded": true, "account_restricted": true}
	if allowed[payload.Error.Code] {
		return fmt.Sprintf("upstream_http_%d: %s", status, payload.Error.Code)
	}
	return fmt.Sprintf("upstream_http_%d", status)
}

func recordRejectedRequest(stats *usage.Collector, endpoint string, principal apikey.Principal, clientIP, requestID string, start time.Time, err error) {
	if stats == nil {
		return
	}
	status := http.StatusBadRequest
	if requestErr, ok := openai.AsRequestError(err); ok && requestErr.Status > 0 {
		status = requestErr.Status
	}
	request := openai.Request{Path: endpoint}
	if endpoint != openai.ModelsPath {
		stats.Record("未指定", status, 0, err)
	}
	stats.RecordRequest(usage.RequestRecord{
		APIKeyID: principal.ID, APIKeyName: principal.Name, ClientIP: clientIP, RequestID: requestID,
		AuthResult: "authorized", DurationMS: time.Since(start).Milliseconds(),
		CreatedAt: start,
		Model:     "未指定",
		Endpoint:  endpoint,
		Type:      requestType(request),
		Status:    status,
		Success:   false,
		Error:     requestErrorText(err),
	})
}

func (w *trackingResponseWriter) Unwrap() http.ResponseWriter {
	return w.ResponseWriter
}

func adminHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data: https:; font-src 'self'; connect-src 'self'; frame-ancestors 'none'; base-uri 'self'; form-action 'self'")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "same-origin")
		w.Header().Set("Cache-Control", "no-cache")
		next.ServeHTTP(w, r)
	})
}

func writeRequestError(w http.ResponseWriter, err error) {
	status := http.StatusBadRequest
	message := "invalid request"
	param := ""
	if requestErr, ok := openai.AsRequestError(err); ok {
		status = requestErr.Status
		message = requestErr.Message
		param = requestErr.Param
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(struct {
		Error struct {
			Message string `json:"message"`
			Type    string `json:"type"`
			Param   string `json:"param,omitempty"`
		} `json:"error"`
	}{
		Error: struct {
			Message string `json:"message"`
			Type    string `json:"type"`
			Param   string `json:"param,omitempty"`
		}{
			Message: message,
			Type:    "invalid_request_error",
			Param:   param,
		},
	})
}

func requestRemoteIP(r *http.Request) string {
	remote := strings.TrimSpace(r.RemoteAddr)
	if host, _, err := net.SplitHostPort(remote); err == nil {
		return host
	}
	return strings.Trim(remote, "[]")
}

func writeAuthError(w http.ResponseWriter, authErr *apikey.AuthError) {
	status := http.StatusUnauthorized
	if authErr != nil && authErr.Status >= 400 {
		status = authErr.Status
	}
	if authErr != nil && authErr.RetryAfter > 0 {
		w.Header().Set("Retry-After", strconv.Itoa(authErr.RetryAfter))
	}
	w.Header().Set("WWW-Authenticate", `Bearer realm="clodesen2api"`)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	type authBody struct {
		Message string `json:"message"`
		Type    string `json:"type"`
		Code    string `json:"code"`
	}
	message, code, kind := "API key is invalid", "invalid_api_key", "authentication_error"
	if authErr != nil {
		message, code = authErr.Message, authErr.Code
		if status == http.StatusForbidden {
			kind = "permission_error"
		} else if status == http.StatusTooManyRequests {
			kind = "rate_limit_error"
		}
	}
	_ = json.NewEncoder(w).Encode(struct {
		Error authBody `json:"error"`
	}{Error: authBody{Message: message, Type: kind, Code: code}})
}

func recordRejectedAuth(stats *usage.Collector, endpoint, model string, principal apikey.Principal, clientIP, requestID string, start time.Time, authErr *apikey.AuthError) {
	if stats == nil || authErr == nil {
		return
	}
	if model == "" {
		model = "未指定"
	}
	if endpoint != openai.ModelsPath {
		stats.Record(model, authErr.Status, time.Since(start), authErr)
	}
	now := time.Now()
	stats.RecordRequest(usage.RequestRecord{
		CreatedAt: now, Model: model, Endpoint: endpoint,
		APIKeyID: principal.ID, APIKeyName: principal.Name, ClientIP: clientIP, RequestID: requestID,
		AuthResult: "rejected", AuthErrorCode: authErr.Code, DurationMS: time.Since(start).Milliseconds(),
		Type: "unknown", Status: authErr.Status, Success: false, Error: authErr.Error(),
	})
}
