package apikey

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestStoreCreatesHashOnlyKeyAndEnforcesLimits(t *testing.T) {
	path := filepath.Join(t.TempDir(), "api-keys.json")
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	rpm := 1
	item, secret, err := store.Create(CreateRequest{
		Name:    "client-a",
		RPM:     &rpm,
		Models:  []string{"gpt-4.1"},
		IPAllow: []string{"127.0.0.0/8"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(secret, "sk-") || item.Prefix != secret[:8] {
		t.Fatalf("unexpected secret/prefix: %q / %q", secret, item.Prefix)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), secret) {
		t.Fatal("API key secret was persisted in plaintext")
	}
	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, authErr := reopened.Authenticate(secret, "gpt-4.1", "127.0.0.1"); authErr != nil {
		t.Fatalf("reopened store rejected valid key: %+v", authErr)
	}

	if _, authErr := store.Authenticate(secret, "gpt-4.1", "127.0.0.1"); authErr != nil {
		t.Fatalf("valid key rejected: %+v", authErr)
	}
	if _, authErr := store.Authenticate(secret, "gpt-4.1", "127.0.0.1"); authErr == nil || authErr.Status != 429 {
		t.Fatalf("second request error = %+v, want 429", authErr)
	}
	if _, authErr := store.Authenticate(secret, "gpt-4o", "127.0.0.1"); authErr == nil || authErr.Status != 403 || authErr.Code != "model_not_allowed" {
		t.Fatalf("model restriction error = %+v", authErr)
	}
	if _, authErr := store.Authenticate(secret, "gpt-4.1", "10.0.0.1"); authErr == nil || authErr.Status != 403 || authErr.Code != "ip_not_allowed" {
		t.Fatalf("IP restriction error = %+v", authErr)
	}
}

func TestStoreRotateAndDisable(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "api-keys.json"))
	if err != nil {
		t.Fatal(err)
	}
	item, oldSecret, err := store.Create(CreateRequest{Name: "client-b"})
	if err != nil {
		t.Fatal(err)
	}
	rotated, newSecret, err := store.Rotate(item.ID, item.Version)
	if err != nil {
		t.Fatal(err)
	}
	if rotated.Version != 2 || newSecret == oldSecret {
		t.Fatalf("rotation = version %d, old=%q new=%q", rotated.Version, oldSecret, newSecret)
	}
	if _, authErr := store.Authenticate(oldSecret, "", "127.0.0.1"); authErr == nil || authErr.Code != "invalid_api_key" {
		t.Fatalf("old secret still accepted: %+v", authErr)
	}
	if _, authErr := store.Authenticate(newSecret, "", "127.0.0.1"); authErr != nil {
		t.Fatalf("new secret rejected: %+v", authErr)
	}
	disabled := false
	updated, err := store.Update(item.ID, rotated.Version, UpdateRequest{Version: rotated.Version, Enabled: &disabled})
	if err != nil {
		t.Fatal(err)
	}
	if updated.Enabled {
		t.Fatal("key remained enabled")
	}
	if _, authErr := store.Authenticate(newSecret, "", "127.0.0.1"); authErr == nil || authErr.Code != "api_key_disabled" {
		t.Fatalf("disabled key accepted: %+v", authErr)
	}
}

func TestAuthenticatorNoAuthPolicyAndTrustedProxy(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "api-keys.json"))
	if err != nil {
		t.Fatal(err)
	}
	auth, err := NewAuthenticator("", store, false, "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	if _, authErr := auth.Authenticate("127.0.0.1", "", "", "", ""); authErr == nil || authErr.Code != "api_key_required" {
		t.Fatalf("missing key error = %+v", authErr)
	}
	openAuth, err := NewAuthenticator("", store, true, "")
	if err != nil {
		t.Fatal(err)
	}
	empty, err := Open(filepath.Join(t.TempDir(), "empty.json"))
	if err != nil {
		t.Fatal(err)
	}
	openAuth.Store = empty
	if principal, authErr := openAuth.Authenticate("127.0.0.1", "", "", "", ""); authErr != nil || principal.ID != "anonymous" {
		t.Fatalf("allow-no-auth result = %+v / %+v", principal, authErr)
	}

	expires := time.Now().Add(time.Hour)
	_, secret, err := store.Create(CreateRequest{Name: "proxy-client", ExpiresAt: &expires, IPAllow: []string{"192.0.2.0/24"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, authErr := auth.Authenticate("127.0.0.1", "192.0.2.7, 127.0.0.1", "Bearer "+secret, "", ""); authErr != nil {
		t.Fatalf("trusted proxy key rejected: %+v", authErr)
	}
}

func TestUpdateNullClearsLimitsAndOmittedPreservesThem(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "keys.json"))
	if err != nil {
		t.Fatal(err)
	}
	rpm, expires := 12, time.Now().Add(time.Hour)
	item, _, err := store.Create(CreateRequest{Name: "中文客户端", RPM: &rpm, ExpiresAt: &expires, IPAllow: []string{"127.0.0.1", "::1"}})
	if err != nil {
		t.Fatal(err)
	}
	var input UpdateRequest
	if err := json.Unmarshal([]byte(`{"version":1,"name":"renamed"}`), &input); err != nil {
		t.Fatal(err)
	}
	item, err = store.Update(item.ID, input.Version, input)
	if err != nil || item.RPM == nil || item.ExpiresAt == nil {
		t.Fatalf("omitted limits changed: %+v %v", item, err)
	}
	if err := json.Unmarshal([]byte(`{"version":2,"rpm":null,"expiresAt":null,"models":[],"ipAllow":[]}`), &input); err != nil {
		t.Fatal(err)
	}
	item, err = store.Update(item.ID, input.Version, input)
	if err != nil || item.RPM != nil || item.ExpiresAt != nil || len(item.IPAllow) != 0 {
		t.Fatalf("null did not clear limits: %+v %v", item, err)
	}
	if _, err := store.Update(item.ID, 1, input); !errors.Is(err, ErrVersionConflict) {
		t.Fatalf("stale update: %v", err)
	}
	for _, body := range []string{`{"unknown":true}`, `{"enabled":null}`, `{"rpm":1.5}`} {
		if err := json.Unmarshal([]byte(body), &input); err == nil {
			t.Fatalf("invalid update accepted: %s", body)
		}
	}
}

func TestRotationRevokesPreAuthenticatedRequestAndUsagePersists(t *testing.T) {
	path := filepath.Join(t.TempDir(), "keys.json")
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	item, secret, err := store.Create(CreateRequest{Name: "rotation"})
	if err != nil {
		t.Fatal(err)
	}
	auth, err := NewAuthenticator("", store, false, "")
	if err != nil {
		t.Fatal(err)
	}
	principal, authErr := auth.PreAuthenticate("127.0.0.1", "", "Bearer "+secret, "")
	if authErr != nil {
		t.Fatal(authErr)
	}
	ctx := WithPrincipal(context.Background(), principal)
	if got, ok := PrincipalFromContext(ctx); !ok || got.ID != item.ID {
		t.Fatalf("context principal: %+v", got)
	}
	_, nextSecret, err := store.Rotate(item.ID, item.Version)
	if err != nil {
		t.Fatal(err)
	}
	if authErr := auth.Authorize(principal, "model"); authErr == nil || authErr.Code != "invalid_api_key" {
		t.Fatalf("rotated principal accepted: %v", authErr)
	}
	if _, authErr := auth.Authenticate("127.0.0.1", "", "Bearer "+nextSecret, "", "model"); authErr != nil {
		t.Fatal(authErr)
	}
	if err := store.FlushUsage(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	saved, err := reopened.Get(item.ID)
	if err != nil || saved.LastUsedAt == nil {
		t.Fatalf("usage was lost on reopen: %+v %v", saved, err)
	}
	if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("key file permissions: %v %v", info, err)
	}
}

func TestClientIPRejectsSpoofedForwardedPrefix(t *testing.T) {
	auth, err := NewAuthenticator("", nil, false, "127.0.0.1,10.0.0.0/8")
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct{ remote, forwarded, want string }{
		{"127.0.0.1", "192.0.2.1, 198.51.100.7", "198.51.100.7"},
		{"127.0.0.1", "192.0.2.1, 10.0.0.2", "192.0.2.1"},
		{"198.51.100.7", "192.0.2.1", "198.51.100.7"},
		{"127.0.0.1", "192.0.2.1, invalid", "127.0.0.1"},
	} {
		if got := auth.ClientIP(test.remote, test.forwarded); got != test.want {
			t.Errorf("ClientIP(%q,%q)=%q want %q", test.remote, test.forwarded, got, test.want)
		}
	}
}
