package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/cookiejar"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"clodesen2api/internal/controlstore"
)

func TestRetiredAccountRoutingMigration(t *testing.T) {
	for _, strategy := range []string{"weighted", "least_inflight", "latency"} {
		t.Run(strategy, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "accounts.json")
			raw := json.RawMessage(`{"version":1,"strategy":"` + strategy + `","accounts":[{"id":"a","identifier":"a","password":"p","enabled":true,"weight":37}]}`)
			if err := os.WriteFile(path, raw, 0o600); err != nil {
				t.Fatal(err)
			}
			store, err := OpenAccountStore(path, "fixed", nil)
			if err != nil || store.Strategy() != "fixed" {
				t.Fatalf("legacy JSON failed: %v", err)
			}
			if err := store.SetStrategy(strategy); err == nil {
				t.Fatal("retired strategy accepted")
			}
			if err := store.Replace(store.Accounts()); err != nil {
				t.Fatal(err)
			}
			data, _ := os.ReadFile(path)
			if strings.Contains(string(data), "weight") || strings.Contains(string(data), strategy) {
				t.Fatalf("legacy fields persisted: %s", data)
			}
			db, err := controlstore.Open(filepath.Join(dir, "control.sqlite3"))
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			if _, err := db.Save(context.Background(), "accounts", 0, raw); err != nil {
				t.Fatal(err)
			}
			control, err := OpenControlAccounts(db, path, "fixed", nil)
			if err != nil || control.Strategy() != "fixed" {
				t.Fatalf("legacy SQLite failed: %v", err)
			}
			if err := control.UpdateRuntimeState("a", false, nil, ""); err != nil {
				t.Fatal(err)
			}
			var saved map[string]any
			if _, err := db.Load(context.Background(), "accounts", &saved); err != nil {
				t.Fatal(err)
			}
			encoded, _ := json.Marshal(saved)
			if saved["strategy"] != "fixed" || strings.Contains(string(encoded), "weight") {
				t.Fatalf("runtime mutation retained retired fields: %s", encoded)
			}
		})
	}
}

func TestAccountAPIRejectsRetiredRoutingFields(t *testing.T) {
	server, store, up, _ := newAdminTestServer(t)
	jar, _ := cookiejar.New(nil)
	client := &http.Client{Jar: jar}
	requestJSON(t, client, http.MethodPost, server.URL+"/admin/api/session", jsonBody(t, map[string]string{"token": testAdminToken}), "")
	for _, strategy := range []string{"weighted", "least_inflight", "latency"} {
		response, body := requestJSON(t, client, http.MethodPut, server.URL+"/admin/api/settings", jsonBody(t, map[string]string{"strategy": strategy}), server.URL)
		if response.StatusCode != 400 || up.Strategy() != "round_robin" || store.Strategy() != "round_robin" {
			t.Fatalf("retired strategy changed state: %d %s", response.StatusCode, body)
		}
	}
	for _, request := range []struct{ method, path, body string }{
		{http.MethodPost, "/admin/api/accounts", `{"identifier":"new","password":"p","weight":1}`},
		{http.MethodPatch, "/admin/api/accounts/account-one", `{"weight":1}`},
	} {
		response, body := requestJSON(t, client, request.method, server.URL+request.path, request.body, server.URL)
		if response.StatusCode != 400 {
			t.Fatalf("retired field accepted: %d %s", response.StatusCode, body)
		}
	}
	_, body := requestJSON(t, client, http.MethodGet, server.URL+"/admin/api/accounts", "", "")
	if strings.Contains(body, `"weight"`) || len(store.Accounts()) != 1 {
		t.Fatalf("unexpected account state: %s", body)
	}
}
