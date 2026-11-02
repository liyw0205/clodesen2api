// Package redemption schedules the daily use of one configured redemption code.
package redemption

import (
	"context"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"clodesen2api/internal/upstream"
)

type Client interface {
	RedeemCodeAll(context.Context, string) []upstream.RedemptionResult
}

type Scheduler struct {
	client   Client
	at       string
	loc      *time.Location
	code     string
	stopCh   chan struct{}
	stopOnce sync.Once
	mu       sync.Mutex
	lastDate string
}

func NewScheduler(client Client, at, code string) (*Scheduler, error) {
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
	return &Scheduler{client: client, at: at, loc: loc, code: code, stopCh: make(chan struct{})}, nil
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
	s.mu.Unlock()
	log.Printf("[redemption] trigger at %s", now.Format(time.RFC3339))
	results := s.client.RedeemCodeAll(ctx, s.code)
	successes := 0
	completed := false
	for _, result := range results {
		if result.Status == "success" {
			successes++
		}
		if result.Status != "error" {
			completed = true
		}
		log.Printf("[redemption] account=%s status=%s credits=%v", result.AccountID, result.Status, result.Credits)
	}
	if len(results) == 0 {
		return
	}
	if successes > 0 || completed {
		s.mu.Lock()
		s.lastDate = today
		s.mu.Unlock()
	}
}
