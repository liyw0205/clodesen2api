package modelcooldown

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"clodesen2api/internal/controlstore"
)

func TestCooldownIsolationRestartAndFailureFallback(t *testing.T) {
	db, err := controlstore.Open(filepath.Join(t.TempDir(), "control.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	store, err := Open(db)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Record("account-a", "model-a", "120"); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(db)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if wait := reopened.Remaining(ctx, "account-a", "model-a"); wait < 119*time.Second || wait > 120*time.Second {
		t.Fatalf("restored wait %s", wait)
	}
	if reopened.Remaining(ctx, "account-a", "model-b") != 0 || reopened.Remaining(ctx, "account-b", "model-a") != 0 {
		t.Fatal("cooldown escaped account/model scope")
	}
	if RetryAfter("9223372036854775807", time.Now()) != ceiling {
		t.Fatal("huge Retry-After was not clamped")
	}
	if RetryAfter("-10", time.Now()) > ceiling {
		t.Fatal("negative Retry-After overflow")
	}
	db.Close()
	if err := reopened.Record("account-a", "model-b", "60"); err == nil {
		t.Fatal("closed storage accepted write")
	}
	if reopened.Remaining(ctx, "account-a", "model-b") == 0 {
		t.Fatal("failed persistence lost local protection")
	}
	if snapshot := reopened.Snapshot(ctx); !snapshot.Degraded || len(snapshot.Entries) != 2 {
		t.Fatalf("fallback %+v", snapshot)
	}
}
