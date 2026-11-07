package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"clodesen2api/internal/controlstore"
)

func TestQueueCapacityAndDailyScheduleChanges(t *testing.T) {
	s, _ := setup(t)
	ctx := context.Background()
	first, _, err := s.SubmitScheduled(ctx, "redeem", []string{"a"}, map[string]string{"code": "one"}, "2026-11-07")
	if err != nil {
		t.Fatal(err)
	}
	second, duplicate, err := s.SubmitScheduled(ctx, "redeem", []string{"b"}, map[string]string{"code": "two"}, "2026-11-07")
	if err != nil || !duplicate || second.ID != first.ID {
		t.Fatalf("schedule repeated after edit: %+v %t %v", second, duplicate, err)
	}
	for i := 0; i < 31; i++ {
		if _, _, err := s.Submit(ctx, "probe", []string{fmt.Sprintf("target-%d", i)}, nil); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, err := s.Submit(ctx, "probe", []string{"overflow"}, nil); !errors.Is(err, ErrCapacity) {
		t.Fatalf("queue unbounded: %v", err)
	}
}

func TestScheduledReceiptWhenSharingManualJob(t *testing.T) {
	s, _ := setup(t)
	ctx := context.Background()
	manual, _, err := s.Submit(ctx, "checkin", []string{"a"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	date := time.Now().Format("2006-01-02")
	job, duplicate, err := s.SubmitScheduled(ctx, "checkin", []string{"a"}, nil, date)
	if err != nil || !duplicate || job.ID != manual.ID {
		t.Fatalf("manual dedupe: %+v %t %v", job, duplicate, err)
	}
	if err := s.Cancel(ctx, manual.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.mutate(ctx, func(v *state) error { v.Jobs = nil; return nil }); err != nil {
		t.Fatal(err)
	}
	if _, duplicate, err := s.SubmitScheduled(ctx, "checkin", []string{"a"}, nil, date); err != nil || !duplicate {
		t.Fatalf("shared attempt replayed after pruning: %t %v", duplicate, err)
	}
}

func setup(t *testing.T) (*Store, *controlstore.Store) {
	t.Helper()
	db, err := controlstore.Open(filepath.Join(t.TempDir(), "c.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	s, err := Open(db)
	if err != nil {
		t.Fatal(err)
	}
	return s, db
}
func awaitJob(t *testing.T, s *Store, id, want string) Job {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		list, err := s.List(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		for _, j := range list {
			if j.ID == id && j.State == want {
				return j
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("job %s did not reach %s", id, want)
	return Job{}
}

func TestSharedDedupeAndWorkers(t *testing.T) {
	s, db := setup(t)
	other, err := controlstore.Open(db.Path())
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	second, err := Open(other)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	var calls atomic.Int32
	var wg sync.WaitGroup
	ids := make(chan string, 20)
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			store := s
			if i%2 > 0 {
				store = second
			}
			job, _, err := store.Submit(ctx, "checkin", []string{"a", "b"}, nil)
			if err != nil {
				t.Error(err)
				return
			}
			ids <- job.ID
		}(i)
	}
	wg.Wait()
	close(ids)
	var id string
	for current := range ids {
		if id != "" && id != current {
			t.Fatal("duplicate jobs")
		}
		id = current
	}
	execute := func(context.Context, string, string, json.RawMessage) (any, error) {
		calls.Add(1)
		return map[string]bool{"ok": true}, nil
	}
	r1 := Start(ctx, s, execute)
	r2 := Start(ctx, second, execute)
	defer r1.Close()
	defer r2.Close()
	awaitJob(t, s, id, "succeeded")
	if calls.Load() != 2 {
		t.Fatalf("executed %d times", calls.Load())
	}
}
func TestCancelAndUnsafeRetry(t *testing.T) {
	s, _ := setup(t)
	ctx := context.Background()
	started := make(chan struct{})
	var once sync.Once
	r := Start(ctx, s, func(ctx context.Context, kind, target string, params json.RawMessage) (any, error) {
		once.Do(func() { close(started) })
		<-ctx.Done()
		return nil, ctx.Err()
	})
	defer r.Close()
	j, _, err := s.Submit(ctx, "redeem", []string{"a", "b"}, map[string]string{"code": "secret-code"})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("not started")
	}
	if err := s.Cancel(ctx, j.ID); err != nil {
		t.Fatal(err)
	}
	done := awaitJob(t, s, j.ID, "cancelled")
	if done.Items[0].State != "interrupted" {
		t.Fatalf("unknown redemption result: %+v", done)
	}
	if done.Items[1].State != "cancelled" {
		t.Fatalf("unstarted redemption marked unknown: %+v", done.Items[1])
	}
	if _, _, err := s.Retry(ctx, j.ID); !errors.Is(err, ErrUnsafeRetry) {
		t.Fatalf("redemption retry accepted: %v", err)
	}
	raw, _ := json.Marshal(done)
	if string(raw) == "" || json.Valid(raw) == false {
		t.Fatal("invalid view")
	}
	var public map[string]any
	_ = json.Unmarshal(raw, &public)
	if _, ok := public["params"]; ok {
		t.Fatal("secret exposed")
	}
}

func TestScheduledReceiptSurvivesHistoryPruningAndImport(t *testing.T) {
	s, db := setup(t)
	ctx := context.Background()
	job, _, err := s.SubmitScheduled(ctx, "redeem", []string{"a"}, map[string]string{"code": "code"}, time.Now().Format("2006-01-02"))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Cancel(ctx, job.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.mutate(ctx, func(v *state) error { v.Jobs = nil; return nil }); err != nil {
		t.Fatal(err)
	}
	_, duplicate, err := s.SubmitScheduled(ctx, "redeem", []string{"a"}, map[string]string{"code": "code"}, time.Now().Format("2006-01-02"))
	if err != nil || !duplicate {
		t.Fatalf("pruned scheduled work replayed: %v", err)
	}
	path := filepath.Join(filepath.Dir(db.Path()), "redemption-scheduler.json")
	data, _ := json.Marshal(map[string]string{"lastTriggeredDate": time.Now().Format("2006-01-02")})
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := s.ImportScheduler(ctx, path); err != nil {
		t.Fatal(err)
	}
	legacy, duplicate, err := s.SubmitScheduled(ctx, "redeem", []string{"new-account"}, map[string]string{"code": "other-code"}, time.Now().Format("2006-01-02"))
	if err != nil || !duplicate || legacy.State != "interrupted" {
		t.Fatalf("legacy marker ignored: %+v %v", legacy, err)
	}
}
func TestRecoveryKeepsLiveOwner(t *testing.T) {
	s, db := setup(t)
	ctx := context.Background()
	job, _, err := s.Submit(ctx, "refresh", []string{"a"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	r := &Runner{store: s, owner: "live"}
	_, ok, err := r.claim(ctx)
	if err != nil || !ok {
		t.Fatal(err)
	}
	if _, err := Open(db); err != nil {
		t.Fatal(err)
	}
	awaitJob(t, s, job.ID, "running")
	if err := s.mutate(ctx, func(v *state) error { v.Jobs[0].Until = time.Now().Add(-time.Second); return nil }); err != nil {
		t.Fatal(err)
	}
	awaitJob(t, s, job.ID, "interrupted")
	if err := r.owned(ctx, job.ID, func(j *savedJob) error { j.State = "succeeded"; return nil }); err == nil {
		t.Fatal("expired owner overwrote recovery")
	}
}
