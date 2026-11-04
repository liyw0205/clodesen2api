// Package server 提供本地 HTTP 服务和路由。
package server

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"io/fs"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"clodesen2api/internal/openai"
	"clodesen2api/internal/upstream"
	"clodesen2api/internal/usage"
)

//go:embed web
var adminAssets embed.FS

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
	if stats == nil {
		stats = usage.New()
	}
	mux := http.NewServeMux()

	// 健康检查仅由本地处理，不会转发到上游。
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("ok"))
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
		request, err := openai.BuildRequest(w, r)
		if err != nil {
			if r.URL.Path != openai.ModelsPath {
				recordRejectedRequest(stats, r.URL.Path, err)
			}
			writeRequestError(w, err)
			log.Printf("[request] rejected %s %s: %v", r.Method, r.URL.Path, err)
			return
		}

		start := time.Now()
		response := &trackingResponseWriter{ResponseWriter: w}
		var forwardErr error
		var wrote bool
		if request.Path != openai.ModelsPath {
			defer func() {
				duration := time.Since(start)
				stats.Record(request.Model, response.status, duration, forwardErr)
				recordedError := requestErrorText(forwardErr)
				if recordedError == "" && len(response.errorBody) > 0 {
					recordedError = strings.TrimSpace(string(response.errorBody))
				}
				var firstTokenMS *int64
				if requestType(request) != "image" && !response.firstByteAt.IsZero() {
					value := response.firstByteAt.Sub(start).Milliseconds()
					firstTokenMS = &value
				}
				stats.RecordRequest(usage.RequestRecord{
					CreatedAt: start, Model: request.Model, DurationMS: duration.Milliseconds(),
					FirstTokenMS: firstTokenMS, ThinkingLevel: request.ThinkingLevel, Prompt: request.Prompt,
					AccountID: response.accountID, Account: response.account,
					Endpoint: request.Path, Type: requestType(request), Status: response.status,
					Success: response.status >= http.StatusOK && response.status < http.StatusMultipleChoices,
					Error:   recordedError, Parameters: request.Parameters,
				})
			}()
		}
		forwardErr, wrote = up.Forward(r.Context(), response, request)
		cost := time.Since(start)
		if forwardErr != nil {
			// 尽量不覆盖已经写出的上游响应
			if errors.Is(forwardErr, context.Canceled) {
				log.Printf("[forward] client canceled %s %s cost=%s", r.Method, r.URL.Path, cost)
				return
			}
			log.Printf("[forward] error %s %s cost=%s: %v", r.Method, r.URL.Path, cost, forwardErr)
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
				response.WriteHeader(http.StatusBadGateway)
				_ = json.NewEncoder(response).Encode(map[string]string{
					"error": forwardErr.Error(),
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
	return err.Error()
}

func recordRejectedRequest(stats *usage.Collector, endpoint string, err error) {
	if stats == nil {
		return
	}
	status := http.StatusBadRequest
	if requestErr, ok := openai.AsRequestError(err); ok && requestErr.Status > 0 {
		status = requestErr.Status
	}
	request := openai.Request{Path: endpoint}
	now := time.Now()
	stats.Record("未指定", status, 0, err)
	stats.RecordRequest(usage.RequestRecord{
		CreatedAt: now,
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
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:; font-src 'self'; connect-src 'self'; frame-ancestors 'none'; base-uri 'self'; form-action 'self'")
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
