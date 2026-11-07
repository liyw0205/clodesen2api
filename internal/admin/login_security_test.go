package admin

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"clodesen2api/internal/apikey"
	"clodesen2api/internal/controlstore"
	"clodesen2api/internal/upstream"
)

func TestLoginLimiterSharedPersistenceAndRecovery(t *testing.T) {
	db, err := controlstore.Open(filepath.Join(t.TempDir(), "control.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	service := func() *Service { s := &Service{}; s.SetControlStore(db); return s }
	one, two := service(), service()
	now := time.Now().UTC()
	for i := 0; i < 5; i++ {
		if wait, err := one.admitLogin(context.Background(), "192.0.2.1", now); err != nil || wait != 0 {
			t.Fatalf("attempt %d: %v %v", i, wait, err)
		}
	}
	if wait, err := two.admitLogin(context.Background(), "192.0.2.1", now); err != nil || wait != 30*time.Second {
		t.Fatalf("shared limit: %v %v", wait, err)
	}
	if wait, err := two.admitLogin(context.Background(), "192.0.2.1", now.Add(30*time.Second)); err != nil || wait != 0 {
		t.Fatalf("retry boundary: %v %v", wait, err)
	}
	if wait, _ := one.admitLogin(context.Background(), "192.0.2.1", now.Add(30*time.Second)); wait != time.Minute {
		t.Fatalf("backoff=%v", wait)
	}
	if err := two.recordLogin(context.Background(), "192.0.2.1", "success", now); err != nil {
		t.Fatal(err)
	}
	if wait, err := service().admitLogin(context.Background(), "192.0.2.1", now); err != nil || wait != 0 {
		t.Fatalf("successful reset: %v %v", wait, err)
	}
	if wait, err := service().admitLogin(context.Background(), "192.0.2.1", now.Add(31*time.Minute)); err != nil || wait != 0 {
		t.Fatalf("expiry: %v %v", wait, err)
	}
	var saved loginState
	if _, err := db.Load(context.Background(), loginSecurityDocument, &saved); err != nil {
		t.Fatal(err)
	}
	if saved.Attempts["192.0.2.1"].Count != 1 || len(saved.Events) != 1 {
		t.Fatalf("saved: %+v", saved)
	}
}

func TestLoginLimiterConcurrentAndCapacity(t *testing.T) {
	s := &Service{}
	now := time.Now()
	var allowed atomic.Int32
	var group sync.WaitGroup
	for i := 0; i < 40; i++ {
		group.Add(1)
		go func() {
			defer group.Done()
			wait, err := s.admitLogin(context.Background(), "192.0.2.2", now)
			if err == nil && wait == 0 {
				allowed.Add(1)
			}
		}()
	}
	group.Wait()
	if allowed.Load() != 5 {
		t.Fatalf("concurrent allowed=%d", allowed.Load())
	}
	for i := 0; i < 4096; i++ {
		s.loginSecurity.state.Attempts[fmt.Sprintf("10.0.%d.%d", i/256, i%256)] = loginAttempt{Count: 1, Expires: now.Add(time.Minute)}
	}
	if wait, _ := s.admitLogin(context.Background(), "192.0.2.3", now); wait != time.Minute {
		t.Fatalf("capacity wait=%v", wait)
	}
	if wait, _ := s.admitLogin(context.Background(), "192.0.2.3", now.Add(31*time.Minute)); wait != 0 {
		t.Fatalf("capacity expiry=%v", wait)
	}
}

func TestLoginIPAndEvents(t *testing.T) {
	s := NewServiceWithConfig(nil, nil, testAdminToken, "", nil)
	r := httptest.NewRequest("GET", "/", nil)
	r.RemoteAddr = "192.0.2.10:1234"
	r.Header.Set("X-Forwarded-For", "198.51.100.2")
	if got := s.loginIP(r); got != "192.0.2.10" {
		t.Fatalf("spoofed XFF: %s", got)
	}
	s.loginSecurity.resolver, _ = apikey.NewAuthenticator("", nil, false, "192.0.2.0/24")
	if got := s.loginIP(r); got != "198.51.100.2" {
		t.Fatalf("trusted XFF: %s", got)
	}
	now := time.Now()
	for i := 0; i < 110; i++ {
		if err := s.recordLogin(context.Background(), "192.0.2.1", "failure", now.Add(time.Duration(i)*time.Millisecond)); err != nil {
			t.Fatal(err)
		}
	}
	w := httptest.NewRecorder()
	s.listLoginEvents(w, httptest.NewRequest("GET", "/admin/api/login-events", nil))
	var response struct {
		Events []loginEvent `json:"events"`
	}
	json.Unmarshal(w.Body.Bytes(), &response)
	if w.Code != 200 || len(response.Events) != 100 || !response.Events[0].Time.After(response.Events[99].Time) {
		t.Fatalf("events: %d %s", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), testAdminToken) {
		t.Fatal("token in login events")
	}
	db, err := controlstore.Open(filepath.Join(t.TempDir(), "control.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	s.SetControlStore(db)
	w = httptest.NewRecorder()
	s.listLoginEvents(w, r)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"events":[]`) {
		t.Fatalf("legacy events: %d %s", w.Code, w.Body.String())
	}
}

func TestLoginHTTPThrottleAndConcurrentSetup(t *testing.T) {
	accounts, err := OpenAccountStore(filepath.Join(t.TempDir(), "accounts.json"), "fixed", []upstream.AccountCredential{})
	if err != nil {
		t.Fatal(err)
	}
	s := NewServiceWithConfig(nil, accounts, testAdminToken, "", nil)
	for i := 0; i < 6; i++ {
		w := httptest.NewRecorder()
		r := httptest.NewRequest("POST", "/admin/api/session", strings.NewReader(`{"token":"incorrect"}`))
		s.ServeHTTP(w, r)
		want := 401
		if i == 5 {
			want = 429
		}
		if w.Code != want || (i == 5 && w.Header().Get("Retry-After") == "") {
			t.Fatalf("attempt %d: %d %s", i, w.Code, w.Body.String())
		}
	}
	for trial := 0; trial < 5; trial++ {
		s = NewServiceWithConfig(nil, accounts, "", filepath.Join(t.TempDir(), "admin.token"), nil)
		start := make(chan struct{})
		var group sync.WaitGroup
		for i := 0; i < 32; i++ {
			group.Add(1)
			go func(i int) {
				defer group.Done()
				<-start
				token := fmt.Sprintf("setup-%04d", i)
				r := httptest.NewRequest("POST", "/admin/api/session", strings.NewReader(fmt.Sprintf(`{"token":%q,"confirm":%q}`, token, token)))
				r.RemoteAddr = fmt.Sprintf("192.0.2.%d:1234", i+1)
				w := httptest.NewRecorder()
				s.ServeHTTP(w, r)
				if w.Code == 200 && s.currentToken() != token {
					t.Errorf("concurrent setup accepted a different token")
				}
			}(i)
		}
		close(start)
		group.Wait()
	}
}
