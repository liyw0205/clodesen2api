package admin

import (
	"path/filepath"
	"testing"
	"time"

	"clodesen2api/internal/upstream"
)

func TestAccountStoreUpdateRuntimeStatePersistsCooldown(t *testing.T) {
	path := filepath.Join(t.TempDir(), "accounts.json")
	initial := []upstream.AccountCredential{{
		ID: "account-1", Identifier: "user@example.test", Password: "secret", Enabled: true,
	}}
	store, err := OpenAccountStore(path, "round_robin", initial)
	if err != nil {
		t.Fatalf("OpenAccountStore() error = %v", err)
	}
	until := time.Now().Add(47 * time.Minute).UTC().Truncate(time.Second)
	if err := store.UpdateRuntimeState("account-1", true, &until, "temporary_ban"); err != nil {
		t.Fatalf("UpdateRuntimeState() error = %v", err)
	}

	reloaded, err := OpenAccountStore(path, "round_robin", nil)
	if err != nil {
		t.Fatalf("reload account store: %v", err)
	}
	accounts := reloaded.Accounts()
	if len(accounts) != 1 || accounts[0].CooldownUntil == nil {
		t.Fatalf("reloaded accounts = %+v, want one account with cooldown", accounts)
	}
	if !accounts[0].CooldownUntil.Equal(until) {
		t.Fatalf("cooldown = %s, want %s", accounts[0].CooldownUntil, until)
	}
	if accounts[0].CooldownReason != "temporary_ban" {
		t.Fatalf("cooldown reason = %q, want temporary_ban", accounts[0].CooldownReason)
	}
}

func TestAccountStoreUpdateRuntimeStatePersistsPermanentDisable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "accounts.json")
	initial := []upstream.AccountCredential{{
		ID: "account-1", Identifier: "user@example.test", Password: "secret", Enabled: true,
	}}
	store, err := OpenAccountStore(path, "round_robin", initial)
	if err != nil {
		t.Fatalf("OpenAccountStore() error = %v", err)
	}
	if err := store.UpdateRuntimeState("account-1", false, nil, ""); err != nil {
		t.Fatalf("UpdateRuntimeState() error = %v", err)
	}
	reloaded, err := OpenAccountStore(path, "round_robin", nil)
	if err != nil {
		t.Fatalf("reload account store: %v", err)
	}
	accounts := reloaded.Accounts()
	if len(accounts) != 1 || accounts[0].Enabled || accounts[0].CooldownUntil != nil {
		t.Fatalf("reloaded accounts = %+v, want disabled account without cooldown", accounts)
	}
}
