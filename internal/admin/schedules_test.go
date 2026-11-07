package admin

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"clodesen2api/internal/config"
	"clodesen2api/internal/controlstore"
	"clodesen2api/internal/jobs"
	"clodesen2api/internal/upstream"
)

func scheduleFixture(t *testing.T) (*Service, *controlstore.Store) {
	t.Helper()
	db, err := controlstore.Open(filepath.Join(t.TempDir(), "control.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	cfg := &config.Config{}
	cfg.Server.Addr = "127.0.0.1:6687"
	cfg.Upstream.BaseURL, cfg.Upstream.OpenAIBaseURL, cfg.Upstream.LoginPath, cfg.Upstream.LoginTimeout = "https://example.test", "https://example.test/openai", "/login", 30*time.Second
	cfg.Auth.Strategy, cfg.Auth.RPM = "fixed", 4
	cfg.Checkin.Enable, cfg.Checkin.Path, cfg.Checkin.At, cfg.Checkin.Timezone = true, "/checkin", "00:00", "Asia/Shanghai"
	cfg.Redemption.Enable, cfg.Redemption.At, cfg.Redemption.Code = true, "00:00", "code"
	settings, err := config.OpenSettings(context.Background(), db, cfg)
	if err != nil {
		t.Fatal(err)
	}
	queue, err := jobs.Open(db)
	if err != nil {
		t.Fatal(err)
	}
	return &Service{cfg: cfg, settings: settings, jobs: queue, store: &AccountStore{accounts: []upstream.AccountCredential{{ID: "a", Enabled: true}}}}, db
}

func TestSchedulesWaitUntilConfiguredTimeOnStartup(t *testing.T) {
	s, _ := scheduleFixture(t)
	loc, _ := time.LoadLocation("Asia/Shanghai")
	now := time.Date(2026, 11, 7, 12, 0, 0, 0, loc)
	view, _ := s.settings.View(context.Background())
	view.Checkin.At, view.Redemption.At = "23:59", "23:59"
	if _, _, err := s.settings.Save(context.Background(), view); err != nil {
		t.Fatal(err)
	}
	s.scheduleJobs(context.Background(), now)
	list, err := s.jobs.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 0 {
		t.Fatalf("startup ran future schedules: %+v", list)
	}
}

func TestSchedulesOncePerDayAndExplicitFutureRearm(t *testing.T) {
	for _, success := range []bool{false, true} {
		t.Run(map[bool]string{false: "failure", true: "success"}[success], func(t *testing.T) {
			s, db := scheduleFixture(t)
			ctx := context.Background()
			loc, _ := time.LoadLocation("Asia/Shanghai")
			now := time.Now().In(loc)
			future := now.Add(2 * time.Minute).Truncate(time.Minute)
			if now.Day() != future.Day() {
				t.Skip("future time falls on tomorrow")
			}
			runner := jobs.Start(ctx, s.jobs, func(context.Context, string, string, json.RawMessage) (any, error) {
				if !success {
					return nil, errors.New("upstream rejected")
				}
				return nil, nil
			})
			defer runner.Close()
			assertCount := func(want int) {
				t.Helper()
				list, err := s.jobs.List(ctx)
				if err != nil {
					t.Fatal(err)
				}
				if len(list) != want {
					t.Fatalf("jobs=%d want=%d: %+v", len(list), want, list)
				}
			}
			waitDone := func() {
				t.Helper()
				deadline := time.Now().Add(5 * time.Second)
				for time.Now().Before(deadline) {
					list, err := s.jobs.List(ctx)
					if err != nil {
						t.Fatal(err)
					}
					active := false
					for _, job := range list {
						active = active || job.State == "queued" || job.State == "running"
					}
					if !active {
						return
					}
					time.Sleep(10 * time.Millisecond)
				}
				t.Fatal("scheduled jobs did not finish")
			}
			s.scheduleJobs(ctx, now)
			waitDone()
			assertCount(2)
			for i := 0; i < 3; i++ {
				s.scheduleJobs(ctx, now)
			}
			assertCount(2)
			view, _ := s.settings.View(ctx)
			view.Redemption.Code = "changed-code"
			view.Checkin.At, view.Redemption.At = "00:01", "00:01"
			if _, _, err := s.settings.Save(ctx, view); err != nil {
				t.Fatal(err)
			}
			s.scheduleJobs(ctx, now)
			assertCount(2)
			view, _ = s.settings.View(ctx)
			view.Checkin.At, view.Redemption.At = future.Format("15:04"), future.Format("15:04")
			if _, _, err := s.settings.Save(ctx, view); err != nil {
				t.Fatal(err)
			}
			s.scheduleJobs(ctx, now)
			assertCount(2)
			// Reopening the stores must retain both the daily receipt and rearm.
			restarted, err := config.OpenSettings(ctx, db, s.cfg)
			if err != nil {
				t.Fatal(err)
			}
			s.settings = restarted
			s.jobs, err = jobs.Open(db)
			if err != nil {
				t.Fatal(err)
			}
			s.scheduleJobs(ctx, future)
			waitDone()
			assertCount(4)
			s.scheduleJobs(ctx, future.Add(time.Minute))
			assertCount(4)
		})
	}
}
