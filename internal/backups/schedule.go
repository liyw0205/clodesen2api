package backups

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"clodesen2api/internal/controlstore"
)

const ScheduleDocument = "backup-schedule"

type ScheduleSettings struct {
	Enabled  bool   `json:"enabled"`
	At       string `json:"at"`
	Timezone string `json:"timezone"`
	Retain   int    `json:"retain"`
}

type ScheduleView struct {
	Schema      int              `json:"schema"`
	Revision    int64            `json:"revision"`
	Settings    ScheduleSettings `json:"settings"`
	Date        string           `json:"date,omitempty"`
	Claim       string           `json:"claim,omitempty"`
	LeaseUntil  time.Time        `json:"leaseUntil"`
	RetryAt     time.Time        `json:"retryAt"`
	Attempts    int              `json:"attempts"`
	Completed   bool             `json:"completed"`
	LastAttempt *time.Time       `json:"lastAttempt,omitempty"`
	LastSuccess *time.Time       `json:"lastSuccess,omitempty"`
	LastError   string           `json:"lastError,omitempty"`
	LastWarning string           `json:"lastWarning,omitempty"`
	LastBackup  string           `json:"lastBackup,omitempty"`
}

func defaultSchedule() ScheduleView {
	return ScheduleView{Schema: 1, Revision: 1, Settings: ScheduleSettings{At: "03:00", Timezone: "Asia/Shanghai", Retain: 7}}
}
func (v ScheduleSettings) validate() error {
	if _, err := time.Parse("15:04", v.At); err != nil {
		return errors.New("备份时间必须为 HH:MM")
	}
	if _, err := time.LoadLocation(v.Timezone); err != nil {
		return errors.New("备份时区无效")
	}
	if v.Retain < 1 || v.Retain > 60 {
		return errors.New("自动备份保留数量必须在 1 到 60 之间")
	}
	return nil
}
func decodeSchedule(raw json.RawMessage) (ScheduleView, error) {
	value := defaultSchedule()
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &value); err != nil {
			return value, err
		}
	}
	if value.Schema != 1 || value.Revision < 1 || value.Attempts < 0 || value.Attempts > 20 {
		return value, errors.New("invalid backup schedule")
	}
	if value.LastError != "" && value.LastError != "backup_failed" {
		return value, errors.New("invalid backup schedule error")
	}
	if value.LastWarning != "" && value.LastWarning != "cleanup_failed" {
		return value, errors.New("invalid backup warning")
	}
	if value.LastBackup != "" && !validID(value.LastBackup) {
		return value, errors.New("invalid scheduled backup ID")
	}
	if value.Date != "" {
		if _, err := time.Parse("2006-01-02", value.Date); err != nil {
			return value, err
		}
	}
	return value, value.Settings.validate()
}
func ValidateScheduleDocument(raw json.RawMessage) error { _, err := decodeSchedule(raw); return err }

func (s *Service) Schedule(ctx context.Context) (ScheduleView, error) {
	var raw json.RawMessage
	if _, err := s.control.Load(ctx, ScheduleDocument, &raw); err != nil && !errors.Is(err, controlstore.ErrMissing) {
		return ScheduleView{}, err
	}
	return decodeSchedule(raw)
}
func (s *Service) SaveSchedule(ctx context.Context, revision int64, settings ScheduleSettings) (ScheduleView, error) {
	if err := settings.validate(); err != nil {
		return ScheduleView{}, err
	}
	var saved ScheduleView
	err := s.control.Mutate(ctx, ScheduleDocument, func(raw json.RawMessage) (any, error) {
		value, err := decodeSchedule(raw)
		if err != nil {
			return nil, err
		}
		if value.Revision != revision {
			return nil, controlstore.ErrConflict
		}
		if value.Settings.At != settings.At || value.Settings.Timezone != settings.Timezone {
			value.Date = ""
			value.Completed = false
			value.RetryAt = time.Time{}
		}
		value.Revision++
		value.Settings = settings
		saved = value
		return value, nil
	})
	return saved, err
}

// RunScheduled claims one daily attempt across instances, with bounded retries.
func (s *Service) RunScheduled(ctx context.Context, now time.Time, before ...func(context.Context) error) (bool, error) {
	initial, err := s.Schedule(ctx)
	if err != nil || !initial.Settings.Enabled {
		return false, err
	}
	id, err := randomID()
	if err != nil {
		return false, err
	}
	claimed := false
	var keep int
	err = s.control.Mutate(ctx, ScheduleDocument, func(raw json.RawMessage) (any, error) {
		value, err := decodeSchedule(raw)
		if err != nil {
			return nil, err
		}
		if !value.Settings.Enabled || value.LeaseUntil.After(now) {
			return value, nil
		}
		loc, _ := time.LoadLocation(value.Settings.Timezone)
		local := now.In(loc)
		clock, _ := time.Parse("15:04", value.Settings.At)
		due := time.Date(local.Year(), local.Month(), local.Day(), clock.Hour(), clock.Minute(), 0, 0, loc)
		if local.Before(due) {
			return value, nil
		}
		date := local.Format("2006-01-02")
		if value.Date != date {
			value.Date = date
			value.Completed = false
			value.Attempts = 0
			value.RetryAt = time.Time{}
		}
		if value.Completed || value.Attempts >= 10 || value.RetryAt.After(now) {
			return value, nil
		}
		value.Claim = id
		value.LeaseUntil = now.Add(10 * time.Minute)
		value.Attempts++
		value.LastAttempt = &now
		claimed = true
		keep = value.Settings.Retain
		return value, nil
	})
	if err != nil || !claimed {
		return false, err
	}
	work, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	var createErr error
	for _, prepare := range before {
		if createErr = prepare(work); createErr != nil {
			break
		}
	}
	var record Record
	if createErr == nil {
		record, createErr = s.create(work, true, keep)
	}
	finish, cancelFinish := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelFinish()
	err = s.control.Mutate(finish, ScheduleDocument, func(raw json.RawMessage) (any, error) {
		value, err := decodeSchedule(raw)
		if err != nil {
			return nil, err
		}
		if value.Claim != id {
			return value, nil
		}
		value.Claim = ""
		value.LeaseUntil = time.Time{}
		if createErr != nil {
			value.LastError = "backup_failed"
			value.RetryAt = now.Add(time.Minute * time.Duration(1<<min(value.Attempts-1, 5)))
		} else {
			value.Completed = true
			value.LastSuccess = &now
			value.LastBackup = record.ID
			value.LastWarning = record.Warning
			value.LastError = ""
			value.RetryAt = time.Time{}
		}
		return value, nil
	})
	return true, errors.Join(createErr, err)
}
