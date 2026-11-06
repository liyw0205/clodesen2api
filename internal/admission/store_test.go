package admission

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"clodesen2api/internal/controlstore"
)

func TestLeaseRenewalIsFencedAfterExpiration(t *testing.T) {
	db, err := controlstore.Open(filepath.Join(t.TempDir(), "c.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	s, err := Open(db)
	if err != nil {
		t.Fatal(err)
	}
	l, err := s.Acquire(context.Background(), "key", Policy{}, 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := l.renew(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := s.change(context.Background(), func(v *state) error {
		current := v.Leases[l.id]
		current.Until = time.Now().Add(-time.Second)
		v.Leases[l.id] = current
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := l.renew(context.Background()); err == nil {
		t.Fatal("expired lease resurrected")
	}
	if err := l.Admit(context.Background(), "model", Policy{}, nil); err == nil {
		t.Fatal("expired lease admitted")
	}
	if err := l.Release(); err != nil {
		t.Fatal(err)
	}
}

func TestSharedCapacityAndQuota(t *testing.T) {
	path := filepath.Join(t.TempDir(), "control.sqlite3")
	a, err := controlstore.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	b, err := controlstore.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	x, err := Open(a)
	if err != nil {
		t.Fatal(err)
	}
	y, err := Open(b)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	var admitted atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			s := x
			if i%2 != 0 {
				s = y
			}
			l, err := s.Acquire(ctx, "key", Policy{}, 10)
			if err != nil {
				t.Error(err)
				return
			}
			defer l.Release()
			err = l.Admit(ctx, "model", Policy{DailyRequests: 5}, nil)
			if err == nil {
				admitted.Add(1)
				if err := l.Admit(ctx, "model", Policy{DailyRequests: 5}, nil); err != nil {
					t.Error(err)
				}
			} else {
				var rejected *Error
				if !errors.As(err, &rejected) || rejected.Code != "request_quota_exceeded" {
					t.Error(err)
				}
			}
		}(i)
	}
	wg.Wait()
	if admitted.Load() != 5 {
		t.Fatalf("admitted %d", admitted.Load())
	}
	view, err := y.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if view.Active != 0 || view.BodyBytes != 0 {
		t.Fatalf("leaked: %+v", view)
	}
	l, err := y.Acquire(ctx, "key", Policy{}, 1)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Release()
	if err = l.Admit(ctx, "model", Policy{DailyRequests: 5}, nil); err == nil {
		t.Fatal("quota reset")
	}
}

func TestLeasesAndUnknownMetering(t *testing.T) {
	db, err := controlstore.Open(filepath.Join(t.TempDir(), "c.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	s, err := Open(db)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	l, err := s.Acquire(ctx, "key", Policy{MaxConcurrent: 1}, 10)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Acquire(ctx, "key", Policy{MaxConcurrent: 1}, 10); err == nil {
		t.Fatal("concurrency bypass")
	}
	if err := l.Admit(ctx, "model", Policy{DailyTokens: 10}, nil); err == nil {
		t.Fatal("unknown tokens accepted")
	}
	if err := l.Release(); err != nil {
		t.Fatal(err)
	}
	if err := l.Release(); err != nil {
		t.Fatal(err)
	}
	view, _ := s.Snapshot(ctx)
	if view.Active != 0 || len(view.Counters) != 0 {
		t.Fatalf("rejection charged: %+v", view)
	}
}
