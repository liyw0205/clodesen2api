// Package redemption schedules the daily use of one configured redemption code.
package redemption

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"clodesen2api/internal/upstream"
)

type Client interface {
	RedeemCodeAll(context.Context, string) []upstream.RedemptionResult
}

type Scheduler struct {
	client    Client
	at        string
	loc       *time.Location
	code      string
	statePath string
	stopCh    chan struct{}
	stopOnce  sync.Once
	mu        sync.Mutex
	lastDate  string
}

func NewScheduler(client Client, at, code string) (*Scheduler, error) {
	return NewSchedulerWithStatePath(client, at, code, "")
}

func NewSchedulerWithStatePath(client Client, at, code, statePath string) (*Scheduler, error) {
	if client == nil {
		return nil, fmt.Errorf("redemption client is nil")
	}
	at = strings.TrimSpace(at)
	if _, err := time.Parse("15:04", at); err != nil {
		return nil, fmt.Errorf("invalid redemption.at %q: %w", at, err)
	}
	loc, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		return nil, fmt.Errorf("load Asia/Shanghai: %w", err)
	}
	code = strings.TrimSpace(code)
	if code == "" {
		return nil, fmt.Errorf("redemption code is empty")
	}
	statePath = strings.TrimSpace(statePath)
	lastDate, err := loadSchedulerDate(statePath)
	if err != nil {
		return nil, fmt.Errorf("load redemption scheduler state: %w", err)
	}
	return &Scheduler{
		client: client, at: at, loc: loc, code: code, statePath: statePath,
		stopCh: make(chan struct{}), lastDate: lastDate,
	}, nil
}

func (s *Scheduler) Run(ctx context.Context) {
	s.maybeTick(ctx, time.Now())
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-s.stopCh:
			return
		case now := <-ticker.C:
			s.maybeTick(ctx, now)
		}
	}
}

func (s *Scheduler) Stop() { s.stopOnce.Do(func() { close(s.stopCh) }) }

func (s *Scheduler) maybeTick(ctx context.Context, global time.Time) {
	now := global.In(s.loc)
	hm, err := time.Parse("15:04", s.at)
	if err != nil {
		return
	}
	target := time.Date(now.Year(), now.Month(), now.Day(), hm.Hour(), hm.Minute(), 0, 0, s.loc)
	if now.Before(target) {
		return
	}
	today := now.Format("2006-01-02")
	s.mu.Lock()
	if s.lastDate == today {
		s.mu.Unlock()
		return
	}
	if s.statePath != "" {
		if err := saveSchedulerDate(s.statePath, today); err != nil {
			s.lastDate = today
			s.mu.Unlock()
			log.Printf("[redemption] state persistence failed; skipping today's redemption: %v", err)
			return
		}
	}
	// Record the attempt before calling upstream so errors and restarts cannot
	// cause the same scheduled redemption to be replayed throughout the day.
	s.lastDate = today
	s.mu.Unlock()
	log.Printf("[redemption] trigger at %s", now.Format(time.RFC3339))
	results := s.client.RedeemCodeAll(ctx, s.code)
	for _, result := range results {
		log.Printf("[redemption] account=%s status=%s credits=%v", result.AccountID, result.Status, result.Credits)
	}
}

func loadSchedulerDate(path string) (string, error) {
	if path == "" {
		return "", nil
	}
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	var state struct {
		LastTriggeredDate string `json:"lastTriggeredDate"`
	}
	if err := json.Unmarshal(data, &state); err != nil {
		return "", err
	}
	if state.LastTriggeredDate == "" {
		return "", nil
	}
	if _, err := time.Parse("2006-01-02", state.LastTriggeredDate); err != nil {
		return "", fmt.Errorf("invalid lastTriggeredDate: %w", err)
	}
	return state.LastTriggeredDate, nil
}

func saveSchedulerDate(path, date string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	file, err := os.CreateTemp(filepath.Dir(path), ".redemption-scheduler-*.tmp")
	if err != nil {
		return err
	}
	tempPath := file.Name()
	defer os.Remove(tempPath)
	if err := file.Chmod(0o600); err != nil {
		_ = file.Close()
		return err
	}
	data, err := json.Marshal(struct {
		LastTriggeredDate string `json:"lastTriggeredDate"`
	}{LastTriggeredDate: date})
	if err != nil {
		_ = file.Close()
		return err
	}
	if _, err := file.Write(append(data, '\n')); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	return os.Rename(tempPath, path)
}
