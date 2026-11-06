package apikey

import (
	"context"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"

	"clodesen2api/internal/admission"
	"clodesen2api/internal/controlstore"
)

func TestAdmissionRejectsRotationAfterPreauthentication(t *testing.T) {
	db, err := controlstore.Open(filepath.Join(t.TempDir(), "c.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	store, err := OpenControl(db, "")
	if err != nil {
		t.Fatal(err)
	}
	key, secret, err := store.Create(CreateRequest{Name: "fenced"})
	if err != nil {
		t.Fatal(err)
	}
	auth, _ := NewAuthenticator("", store, false, "")
	p, authErr := auth.PreAuthenticate("127.0.0.1", "", "Bearer "+secret, "")
	if authErr != nil {
		t.Fatal(authErr)
	}
	capacity, err := admission.Open(db)
	if err != nil {
		t.Fatal(err)
	}
	l, err := capacity.Acquire(context.Background(), p.ID, p.Limits, 1)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Release()
	if _, _, err := store.Rotate(key.ID, key.Version); err != nil {
		t.Fatal(err)
	}
	if err := l.AdmitChecked(context.Background(), "model", p.Limits, p.RPM, auth.AdmissionCheck(p, "model")); err == nil {
		t.Fatal("rotated identity admitted")
	}
	view, err := capacity.Snapshot(context.Background())
	if err != nil || len(view.Counters) != 0 {
		t.Fatalf("denial charged quota: %+v %v", view, err)
	}
}

func TestControlStoreImportRevocationAndConflictAcrossInstances(t *testing.T) {
	dir := t.TempDir()
	legacy, err := Open(filepath.Join(dir, "keys.json"))
	if err != nil {
		t.Fatal(err)
	}
	key, secret, err := legacy.Create(CreateRequest{Name: "imported"})
	if err != nil {
		t.Fatal(err)
	}
	db, err := controlstore.Open(filepath.Join(dir, "control.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	one, err := OpenControl(db, legacy.Path())
	if err != nil {
		t.Fatal(err)
	}
	two, err := OpenControl(db, legacy.Path())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := one.Authenticate(secret, "model", "127.0.0.1"); err != nil {
		t.Fatal(err)
	}
	_, newSecret, err := two.Rotate(key.ID, key.Version)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := one.Authenticate(secret, "model", "127.0.0.1"); err == nil {
		t.Fatal("other instance accepted revoked key")
	}
	if _, err := one.Authenticate(newSecret, "model", "127.0.0.1"); err != nil {
		t.Fatal(err)
	}
	if err := one.FlushUsage(); err != nil {
		t.Fatal(err)
	}
	if _, err := two.Update(key.ID, key.Version, UpdateRequest{}); err != ErrVersionConflict {
		t.Fatalf("stale key write: %v", err)
	}
	if len(one.List()) != 1 || len(two.List()) != 1 {
		t.Fatal("import duplicated keys")
	}
}

func TestSharedRPMDoesNotResetOnRestartOrConcurrentInstances(t *testing.T) {
	path := filepath.Join(t.TempDir(), "control.sqlite3")
	one, err := controlstore.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer one.Close()
	two, err := controlstore.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer two.Close()
	a, err := OpenControl(one, "")
	if err != nil {
		t.Fatal(err)
	}
	b, err := OpenControl(two, "")
	if err != nil {
		t.Fatal(err)
	}
	rpm := 5
	_, secret, err := a.Create(CreateRequest{Name: "shared", RPM: &rpm})
	if err != nil {
		t.Fatal(err)
	}
	var accepted atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, denied := []*Store{a, b}[i%2].Authenticate(secret, "model", "127.0.0.1")
			if denied == nil {
				accepted.Add(1)
			} else if denied.Code != "rpm_exceeded" {
				t.Errorf("unexpected denial: %v", denied)
			}
		}(i)
	}
	wg.Wait()
	if accepted.Load() != 5 {
		t.Fatalf("accepted %d", accepted.Load())
	}
	reopened, err := OpenControl(two, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, denied := reopened.Authenticate(secret, "model", "127.0.0.1"); denied == nil || denied.Code != "rpm_exceeded" {
		t.Fatalf("restart reset RPM: %v", denied)
	}
}
