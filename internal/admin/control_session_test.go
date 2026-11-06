package admin

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"clodesen2api/internal/controlstore"
	"clodesen2api/internal/upstream"
)

func TestControlSessionRestartLogoutAndPrivilegeBoundary(t *testing.T) {
	dir := t.TempDir()
	db, err := controlstore.Open(filepath.Join(dir, "control.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	accounts, err := OpenAccountStore(filepath.Join(dir, "accounts.json"), "fixed", []upstream.AccountCredential{})
	if err != nil {
		t.Fatal(err)
	}
	newService := func() *Service {
		s := NewServiceWithConfig(nil, accounts, testAdminToken, "", nil)
		s.SetControlStore(db)
		return s
	}
	one := newService()
	login := httptest.NewRecorder()
	r := httptest.NewRequest("POST", "/admin/api/session", strings.NewReader(`{"token":"`+testAdminToken+`"}`))
	one.ServeHTTP(login, r)
	if login.Code != 200 || len(login.Result().Cookies()) != 1 {
		t.Fatalf("login: %d %s", login.Code, login.Body.String())
	}
	cookie := login.Result().Cookies()[0]
	var saved map[string]savedSession
	if _, err := db.Load(context.Background(), "sessions", &saved); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(saved)
	if strings.Contains(string(raw), cookie.Value) || strings.Contains(string(raw), testAdminToken) || len(one.sessions) != 0 {
		t.Fatal("raw session/token retained in control mode")
	}
	two := newService()
	request := httptest.NewRequest("GET", "/admin/api/keys", nil)
	request.AddCookie(cookie)
	if !two.authenticated(request) {
		t.Fatal("restart lost valid session")
	}
	forged := httptest.NewRequest("GET", "/admin/api/keys", nil)
	forged.Header.Set("Authorization", "Bearer sk-forged-inference-key")
	if two.authenticated(forged) {
		t.Fatal("inference bearer accessed management")
	}
	logout := httptest.NewRecorder()
	request = httptest.NewRequest("DELETE", "/admin/api/session", nil)
	request.AddCookie(cookie)
	two.ServeHTTP(logout, request)
	if logout.Code != 200 || one.authenticated(request) {
		t.Fatalf("logout not shared: %d", logout.Code)
	}
}
