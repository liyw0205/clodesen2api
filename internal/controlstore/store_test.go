package controlstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestSchemaUpgradeKeepsWALSnapshot(t *testing.T) {
	path := filepath.Join(t.TempDir(), "c.sqlite3")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Save(context.Background(), "sample", 0, map[string]int{"value": 7}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec("PRAGMA user_version=1"); err != nil {
		t.Fatal(err)
	}
	upgraded, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer upgraded.Close()
	defer s.Close()
	paths, err := filepath.Glob(filepath.Join(filepath.Dir(path), "pre-migration", "schema-1-*.sqlite3"))
	if err != nil || len(paths) != 1 {
		t.Fatalf("%v %v", paths, err)
	}
	info, err := os.Stat(paths[0])
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("permissions %v %v", info, err)
	}
	db, err := sql.Open("sqlite", fileURI(paths[0], "ro"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var version int
	if err := db.QueryRow("PRAGMA user_version").Scan(&version); err != nil || version != 1 {
		t.Fatalf("original schema %d %v", version, err)
	}
	var payload string
	if err := db.QueryRow("SELECT payload FROM documents WHERE name='sample'").Scan(&payload); err != nil || payload != `{"value":7}` {
		t.Fatalf("snapshot %s %v", payload, err)
	}
}

func TestAuditRetentionRejectsExpiredReplay(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "c.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	event := AuditEvent{ID: "receipt-boundary", CreatedAt: time.Now().Add(-7*24*time.Hour + time.Minute), Finished: true, Count: true, Model: "model", Success: true, Payload: map[string]string{"id": "receipt-boundary"}}
	if err := s.WriteAudit(ctx, []AuditEvent{event}, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec("DELETE FROM audit_requests"); err != nil {
		t.Fatal(err)
	}
	if err := s.WriteAudit(ctx, []AuditEvent{event}, nil); err != nil {
		t.Fatal(err)
	}
	event.CreatedAt = time.Now().Add(-7*24*time.Hour - time.Minute)
	if _, err := s.db.Exec("DELETE FROM audit_receipts"); err != nil {
		t.Fatal(err)
	}
	if err := s.WriteAudit(ctx, []AuditEvent{event}, nil); err != nil {
		t.Fatal(err)
	}
	totals, err := s.AuditTotals(ctx)
	if err != nil || len(totals) != 1 || totals[0].Requests != 1 {
		t.Fatalf("replayed: %+v %v", totals, err)
	}
}

func TestRevisionAndConcurrentProcesses(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "control.sqlite3")
	one, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer one.Close()
	two, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer two.Close()
	if _, err := one.Save(ctx, "policy", 0, 1); err != nil {
		t.Fatal(err)
	}
	if _, err := two.Save(ctx, "policy", 0, 2); !errors.Is(err, ErrConflict) {
		t.Fatalf("duplicate insert: %v", err)
	}
	if _, err := one.Save(ctx, "policy", 1, 3); err != nil {
		t.Fatal(err)
	}
	if _, err := two.Save(ctx, "policy", 1, 4); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale write: %v", err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 30; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			store := []*Store{one, two}[i%2]
			if err := store.Mutate(ctx, "count", func(raw json.RawMessage) (any, error) {
				var n int
				if len(raw) > 0 {
					if err := json.Unmarshal(raw, &n); err != nil {
						return nil, err
					}
				}
				return n + 1, nil
			}); err != nil {
				t.Error(err)
			}
		}(i)
	}
	wg.Wait()
	var count int
	if _, err := two.Load(ctx, "count", &count); err != nil || count != 30 {
		t.Fatalf("count=%d err=%v", count, err)
	}
}

func TestRejectNewerSchema(t *testing.T) {
	path := filepath.Join(t.TempDir(), "control.sqlite3")
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec("PRAGMA user_version=999"); err != nil {
		t.Fatal(err)
	}
	store.Close()
	if other, err := Open(path); err == nil {
		other.Close()
		t.Fatal("newer schema accepted")
	}
}

func TestAuditImportRollsBackAndRetriesAsOneTransaction(t *testing.T) {
	ctx := context.Background()
	store, err := Open(filepath.Join(t.TempDir(), "control.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, err := store.db.Exec(`CREATE TRIGGER import_failure BEFORE INSERT ON audit_requests BEGIN SELECT RAISE(ABORT,'injected failure'); END`); err != nil {
		t.Fatal(err)
	}
	events := []AuditEvent{{ID: "legacy", CreatedAt: time.Now(), Finished: true, Payload: map[string]string{"id": "legacy"}}}
	if err := store.ImportAudit(ctx, "", map[string]int{"version": 2}, events); err == nil {
		t.Fatal("injected import succeeded")
	}
	if _, err := store.Revision(ctx, "usage"); !errors.Is(err, ErrMissing) {
		t.Fatalf("partial aggregate: %v", err)
	}
	if _, err := store.db.Exec("DROP TRIGGER import_failure"); err != nil {
		t.Fatal(err)
	}
	if err := store.ImportAudit(ctx, "", map[string]int{"version": 2}, events); err != nil {
		t.Fatal(err)
	}
	if err := store.ImportAudit(ctx, "", map[string]int{"version": 999}, events); err != nil {
		t.Fatal(err)
	}
	var saved map[string]int
	if _, err := store.Load(ctx, "usage", &saved); err != nil || saved["version"] != 2 {
		t.Fatalf("import replay: %v %v", saved, err)
	}
	if records, err := store.AuditRecords(ctx); err != nil || len(records) != 1 {
		t.Fatalf("details: %v %v", records, err)
	}
}
