package admin

import (
	"context"
	"log"
	"time"
)

// RunSchedules shares the persistent job queue with manually submitted work.
func (s *Service) RunSchedules(ctx context.Context) {
	s.scheduleJobs(ctx, time.Now())
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			s.scheduleJobs(ctx, now)
		}
	}
}
func (s *Service) scheduleJobs(ctx context.Context, now time.Time) {
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
	checkin, redemption, rearms := s.cfg.Checkin, s.cfg.Redemption, s.cfg.ScheduleRearms
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
	due := func(kind, at, tz string) (string, bool, time.Time) {
		loc, err := time.LoadLocation(tz)
		if err != nil {
			return "", false, time.Time{}
		}
		hm, err := time.Parse("15:04", at)
		if err != nil {
			return "", false, time.Time{}
		}
		local := now.In(loc)
		target := time.Date(local.Year(), local.Month(), local.Day(), hm.Hour(), hm.Minute(), 0, 0, loc)
		rearm := time.Time{}
		if rearms[kind].Equal(target) {
			rearm = rearms[kind]
		}
		return local.Format("2006-01-02"), !local.Before(target), rearm
	}
	if checkin.Enable {
		date, ready, rearm := due("checkin", checkin.At, checkin.Timezone)
		if ready {
			if _, _, err := store.SubmitScheduledRearmed(ctx, "checkin", targets, nil, date, rearm); err != nil {
				log.Printf("[jobs] scheduled checkin submission failed")
			}
		}
	}
	if redemption.Enable && redemption.Code != "" {
		date, ready, rearm := due("redeem", redemption.At, "Asia/Shanghai")
		if ready {
			if _, _, err := store.SubmitScheduledRearmed(ctx, "redeem", targets, map[string]string{"code": redemption.Code}, date, rearm); err != nil {
				log.Printf("[jobs] scheduled redemption submission failed")
			}
		}
	}
}
