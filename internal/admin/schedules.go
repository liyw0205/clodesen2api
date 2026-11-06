package admin

import (
	"context"
	"log"
	"time"
)

// RunSchedules shares the persistent job queue with manually submitted work.
func (s *Service) RunSchedules(ctx context.Context) {
	s.scheduleJobs(ctx, time.Now(), true)
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			s.scheduleJobs(ctx, now, false)
		}
	}
}
func (s *Service) scheduleJobs(ctx context.Context, now time.Time, boot bool) {
	if err := s.SyncSettings(ctx); err != nil {
		log.Printf("[jobs] scheduled settings unavailable")
		return
	}
	s.mu.Lock()
	store := s.jobs
	s.mu.Unlock()
	if store == nil {
		return
	}
	s.configMu.Lock()
	if s.cfg == nil {
		s.configMu.Unlock()
		return
	}
	checkin, redemption := s.cfg.Checkin, s.cfg.Redemption
	s.configMu.Unlock()
	targets := []string{}
	for _, account := range s.store.Accounts() {
		if account.Enabled {
			targets = append(targets, account.ID)
		}
	}
	if len(targets) == 0 {
		return
	}
	due := func(at, tz string) (string, bool) {
		loc, err := time.LoadLocation(tz)
		if err != nil {
			return "", false
		}
		hm, err := time.Parse("15:04", at)
		if err != nil {
			return "", false
		}
		local := now.In(loc)
		target := time.Date(local.Year(), local.Month(), local.Day(), hm.Hour(), hm.Minute(), 0, 0, loc)
		return local.Format("2006-01-02"), !local.Before(target)
	}
	if checkin.Enable {
		date, ready := due(checkin.At, checkin.Timezone)
		if date != "" && (ready || boot) {
			if _, _, err := store.SubmitScheduled(ctx, "checkin", targets, nil, date); err != nil {
				log.Printf("[jobs] scheduled checkin submission failed")
			}
		}
	}
	if redemption.Enable && redemption.Code != "" {
		date, ready := due(redemption.At, "Asia/Shanghai")
		if ready {
			if _, _, err := store.SubmitScheduled(ctx, "redeem", targets, map[string]string{"code": redemption.Code}, date); err != nil {
				log.Printf("[jobs] scheduled redemption submission failed")
			}
		}
	}
}
