package admin

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"clodesen2api/internal/controlstore"
	"clodesen2api/internal/upstream"
)

func TestControlAccountsMigrationAndConflict(t *testing.T) {
	dir := t.TempDir()
	legacy := filepath.Join(dir, "accounts.json")
	initial := []upstream.AccountCredential{{ID: "account", Identifier: "user", Password: "password", Enabled: true}}
	if _, err := OpenAccountStore(legacy, "fixed", initial); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(legacy)
	if err != nil {
		t.Fatal(err)
	}
	db, err := controlstore.Open(filepath.Join(dir, "control.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	x, err := OpenControlAccounts(db, legacy, "fixed", nil)
	if err != nil {
		t.Fatal(err)
	}
	y, err := OpenControlAccounts(db, legacy, "fixed", nil)
	if err != nil {
		t.Fatal(err)
	}
	stale, revision, err := x.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	newAccounts, newRevision, _ := y.Snapshot()
	newAccounts[0].Password = "changed-password"
	if err := y.ReplaceVersion(newAccounts, newRevision); err != nil {
		t.Fatal(err)
	}
	if err := x.ReplaceVersion(stale, revision); !errors.Is(err, controlstore.ErrConflict) {
		t.Fatalf("stale write: %v", err)
	}
	until := time.Now().Add(time.Hour)
	if err := x.UpdateRuntimeState("account", true, &until, "upstream_429"); err != nil {
		t.Fatal(err)
	}
	loaded, _, err := y.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if loaded[0].Password != "changed-password" || loaded[0].CooldownUntil == nil {
		t.Fatal("runtime state overwrote credentials")
	}
	after, _ := os.ReadFile(legacy)
	if string(before) != string(after) {
		t.Fatal("legacy file changed after migration")
	}
	backups, _ := filepath.Glob(filepath.Join(dir, "pre-migration", "accounts-*.json"))
	if len(backups) != 1 {
		t.Fatal("missing migration backup")
	}
}
