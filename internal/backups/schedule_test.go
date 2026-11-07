package backups

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

func TestScheduledBackupDedupRecoveryRetentionAndManualPreservation(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	db, err := controlstore.Open(filepath.Join(dir, "control.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	s, err := New(db, filepath.Join(dir, "backups"), "test", nil)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)
	view, err := s.Schedule(ctx)
	if err != nil || view.Settings.Enabled {
		t.Fatalf("default=%+v %v", view, err)
	}
	if ran, err := s.RunScheduled(ctx, now); ran || err != nil {
		t.Fatal("disabled schedule ran")
	}
	view.Settings.Enabled = true
	view.Settings.At = "13:00"
	view.Settings.Timezone = "UTC"
	view.Settings.Retain = 2
	view, err = s.SaveSchedule(ctx, view.Revision, view.Settings)
	if err != nil {
		t.Fatal(err)
	}
	if ran, err := s.RunScheduled(ctx, now); ran || err != nil {
		t.Fatal("future schedule ran")
	}
	if _, err := s.SaveSchedule(ctx, view.Revision-1, view.Settings); !errors.Is(err, controlstore.ErrConflict) {
		t.Fatal("stale schedule saved")
	}
	view.Settings.At = "00:00"
	view, err = s.SaveSchedule(ctx, view.Revision, view.Settings)
	if err != nil {
		t.Fatal(err)
	}
	manual, err := s.Create(ctx, false)
	if err != nil {
		t.Fatal(err)
	}
	for day := 0; day < 4; day++ {
		var ran atomic.Int32
		var group sync.WaitGroup
		for instance := 0; instance < 4; instance++ {
			group.Add(1)
			go func() {
				defer group.Done()
				other, e := New(db, s.dir, "test", nil)
				if e != nil {
					t.Error(e)
					return
				}
				did, e := other.RunScheduled(ctx, now.Add(time.Duration(day)*24*time.Hour))
				if e != nil {
					t.Error(e)
				}
				if did {
					ran.Add(1)
				}
			}()
		}
		group.Wait()
		if ran.Load() != 1 {
			t.Fatalf("daily attempts=%d", ran.Load())
		}
	}
	list, err := s.List()
	if err != nil || len(list) != 3 {
		t.Fatalf("retention=%d %v", len(list), err)
	}
	found := false
	for _, record := range list {
		if record.ID == manual.ID {
			found = true
		}
	}
	if !found {
		t.Fatal("manual backup pruned")
	}
	latest, err := s.Schedule(ctx)
	if err != nil || latest.LastSuccess == nil || latest.LastError != "" || !latest.Completed {
		t.Fatalf("status=%+v %v", latest, err)
	}
}

func TestScheduledBackupFailureBackoffAndRecovery(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	db, err := controlstore.Open(filepath.Join(dir, "control.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	s, err := New(db, filepath.Join(dir, "backups"), "test", nil)
	if err != nil {
		t.Fatal(err)
	}
	view, _ := s.Schedule(ctx)
	view.Settings.Enabled = true
	view.Settings.At = "00:00"
	view.Settings.Timezone = "UTC"
	if _, err := s.SaveSchedule(ctx, view.Revision, view.Settings); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)
	if ran, err := s.RunScheduled(ctx, now, func(context.Context) error { return errors.New("private path must not appear") }); !ran || err == nil {
		t.Fatal("failure not reported")
	}
	failed, _ := s.Schedule(ctx)
	if failed.LastError != "backup_failed" || failed.Attempts != 1 || !failed.RetryAt.Equal(now.Add(time.Minute)) {
		t.Fatalf("failure=%+v", failed)
	}
	if ran, err := s.RunScheduled(ctx, now.Add(59*time.Second)); ran || err != nil {
		t.Fatal("backoff ignored")
	}
	if ran, err := s.RunScheduled(ctx, now.Add(time.Minute)); !ran || err != nil {
		t.Fatalf("recovery: %v %v", ran, err)
	}
	recovered, _ := s.Schedule(ctx)
	if recovered.LastError != "" || recovered.LastSuccess == nil || recovered.Attempts != 2 {
		t.Fatalf("recovery=%+v", recovered)
	}
}
