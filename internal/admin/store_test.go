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

func TestAccountStoreUsesFixedForNewDefaultAndPreservesSavedStrategy(t *testing.T) {
	path := filepath.Join(t.TempDir(), "accounts.json")
	store, err := OpenAccountStore(path, "", nil)
	if err != nil {
		t.Fatalf("OpenAccountStore() error = %v", err)
	}
	if got := store.Strategy(); got != "fixed" {
		t.Fatalf("new account store strategy = %q, want fixed", got)
	}

	legacyPath := filepath.Join(t.TempDir(), "legacy-accounts.json")
	legacy, err := OpenAccountStore(legacyPath, "round_robin", nil)
	if err != nil {
		t.Fatalf("create saved round-robin account store: %v", err)
	}
	if got := legacy.Strategy(); got != "round_robin" {
		t.Fatalf("explicit initial strategy = %q, want round_robin", got)
	}
	reopened, err := OpenAccountStore(legacyPath, "fixed", nil)
	if err != nil {
		t.Fatalf("reopen saved account store: %v", err)
	}
	if got := reopened.Strategy(); got != "round_robin" {
		t.Fatalf("saved strategy = %q, want preserved round_robin", got)
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
