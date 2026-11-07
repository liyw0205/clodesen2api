package admin

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"strconv"
	"sync"
	"time"

	"clodesen2api/internal/apikey"
	"clodesen2api/internal/controlstore"
)

const loginSecurityDocument = "admin-login-security"

type loginAttempt struct {
	Count   int       `json:"count"`
	Until   time.Time `json:"until"`
	Expires time.Time `json:"expires"`
}
type loginEvent struct {
	Time     time.Time `json:"time"`
	ClientIP string    `json:"clientIP"`
	Event    string    `json:"event"`
}
type loginState struct {
	Version  int                     `json:"version"`
	Attempts map[string]loginAttempt `json:"attempts"`
	Events   []loginEvent            `json:"events"`
}
type loginSecurity struct {
	mu       sync.Mutex
	state    loginState
	resolver *apikey.Authenticator
}

func decodeLoginState(raw json.RawMessage) (loginState, error) {
	value := loginState{Version: 1, Attempts: map[string]loginAttempt{}}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &value); err != nil {
			return value, err
		}
	}
	if value.Version != 1 || len(value.Attempts) > 4096 || len(value.Events) > 100 {
		return value, errors.New("invalid login security document")
	}
	if value.Attempts == nil {
		value.Attempts = map[string]loginAttempt{}
	}
	for ip, attempt := range value.Attempts {
		if net.ParseIP(ip) == nil || attempt.Count < 1 || attempt.Count > 30 || attempt.Expires.IsZero() {
			return value, errors.New("invalid login attempt")
		}
	}
	for _, event := range value.Events {
		if event.Time.IsZero() || net.ParseIP(event.ClientIP) == nil || (event.Event != "success" && event.Event != "failure" && event.Event != "invalid_request" && event.Event != "session_failed") {
			return value, errors.New("invalid login event")
		}
	}
	return value, nil
}

func (s *Service) mutateLogin(ctx context.Context, change func(*loginState)) error {
	s.mu.Lock()
	store := s.control
	s.mu.Unlock()
	if store != nil {
		return store.Mutate(ctx, loginSecurityDocument, func(raw json.RawMessage) (any, error) {
			value, err := decodeLoginState(raw)
			if err != nil {
				return nil, err
			}
			change(&value)
			return value, nil
		})
	}
	s.loginSecurity.mu.Lock()
	defer s.loginSecurity.mu.Unlock()
	if s.loginSecurity.state.Attempts == nil {
		s.loginSecurity.state = loginState{Version: 1, Attempts: map[string]loginAttempt{}}
	}
	change(&s.loginSecurity.state)
	return nil
}

func (s *Service) loginIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	ip := s.loginSecurity.resolver.ClientIP(host, r.Header.Get("X-Forwarded-For"))
	if parsed := net.ParseIP(ip); parsed != nil {
		return parsed.String()
	}
	return "0.0.0.0"
}

func (s *Service) admitLogin(ctx context.Context, ip string, now time.Time) (time.Duration, error) {
	var wait time.Duration
	err := s.mutateLogin(ctx, func(value *loginState) {
		for key, item := range value.Attempts {
			if !item.Expires.After(now) {
				delete(value.Attempts, key)
			}
		}
		item := value.Attempts[ip]
		if item.Until.After(now) {
			wait = item.Until.Sub(now)
			return
		}
		if item.Count == 0 && len(value.Attempts) >= 4096 {
			wait = time.Minute
			return
		}
		item.Count++
		if item.Count > 30 {
			item.Count = 30
		}
		item.Expires = now.Add(30 * time.Minute)
		if item.Count >= 5 {
			delay := 30 * time.Second * time.Duration(1<<min(item.Count-5, 5))
			item.Until = now.Add(min(delay, 15*time.Minute))
		}
		value.Attempts[ip] = item
	})
	return wait, err
}

func (s *Service) recordLogin(ctx context.Context, ip, event string, now time.Time) error {
	return s.mutateLogin(ctx, func(value *loginState) {
		if event == "success" {
			delete(value.Attempts, ip)
		}
		cutoff := now.Add(-7 * 24 * time.Hour)
		events := make([]loginEvent, 0, 100)
		for _, item := range value.Events {
			if item.Time.After(cutoff) {
				events = append(events, item)
			}
		}
		events = append(events, loginEvent{Time: now.UTC(), ClientIP: ip, Event: event})
		if len(events) > 100 {
			events = events[len(events)-100:]
		}
		value.Events = events
	})
}

func (s *Service) listLoginEvents(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", "GET")
		writeError(w, http.StatusMethodNotAllowed, "方法不支持")
		return
	}
	s.mu.Lock()
	store := s.control
	s.mu.Unlock()
	var events []loginEvent
	if store != nil {
		var value loginState
		if _, err := store.Load(r.Context(), loginSecurityDocument, &value); err != nil && !errors.Is(err, controlstore.ErrMissing) {
			writeError(w, http.StatusServiceUnavailable, "登录活动存储不可用")
			return
		}
		events = value.Events
	} else {
		s.loginSecurity.mu.Lock()
		events = append(events, s.loginSecurity.state.Events...)
		s.loginSecurity.mu.Unlock()
	}
	result := make([]loginEvent, 0, len(events))
	for i := len(events) - 1; i >= 0; i-- {
		if events[i].Time.After(time.Now().Add(-7 * 24 * time.Hour)) {
			result = append(result, events[i])
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"events": result})
}

func rejectLoginWait(w http.ResponseWriter, wait time.Duration) {
	seconds := max(1, int((wait+time.Second-1)/time.Second))
	w.Header().Set("Retry-After", strconv.Itoa(seconds))
	writeError(w, http.StatusTooManyRequests, "登录尝试过于频繁，请在 "+strconv.Itoa(seconds)+" 秒后重试")
}
