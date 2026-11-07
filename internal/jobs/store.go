// Package jobs persists bounded administrative work independently of HTTP clients.
package jobs

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"sort"
	"sync"
	"time"

	"clodesen2api/internal/controlstore"
)

const document = "background-jobs"

func ValidateDocument(raw json.RawMessage) error {
	v, err := decode(raw)
	if err != nil {
		return err
	}
	seen := map[string]bool{}
	validState := func(s string) bool {
		switch s {
		case "queued", "running", "succeeded", "partial", "failed", "cancelled", "interrupted":
			return true
		}
		return false
	}
	for _, job := range v.Jobs {
		if job.ID == "" || seen[job.ID] || !validKind(job.Kind) || !validState(job.State) || len(job.Items) < 1 || len(job.Items) > 100 || len(job.Params) > 4096 {
			return errors.New("invalid saved job")
		}
		seen[job.ID] = true
		for _, item := range job.Items {
			if item.Target == "" || !validState(item.State) || len(item.Result) > 8192 {
				return errors.New("invalid saved job item")
			}
		}
	}
	return nil
}

var ErrMissing = errors.New("job not found")
var ErrCapacity = errors.New("job queue capacity exceeded")
var ErrUnsafeRetry = errors.New("job cannot be retried automatically")

type Item struct {
	Target string          `json:"target"`
	State  string          `json:"state"`
	Error  string          `json:"error,omitempty"`
	Result json.RawMessage `json:"result,omitempty"`
}
type Job struct {
	ID              string    `json:"id"`
	Kind            string    `json:"kind"`
	State           string    `json:"state"`
	Items           []Item    `json:"items"`
	CreatedAt       time.Time `json:"createdAt"`
	UpdatedAt       time.Time `json:"updatedAt"`
	CancelRequested bool      `json:"cancelRequested"`
}
type savedJob struct {
	Job
	Params   json.RawMessage `json:"params,omitempty"`
	Dedupe   string          `json:"dedupe"`
	Owner    string          `json:"owner,omitempty"`
	Until    time.Time       `json:"until"`
	Schedule string          `json:"schedule,omitempty"`
}
type state struct {
	Jobs                 []savedJob           `json:"jobs"`
	ScheduleReceipts     map[string]time.Time `json:"scheduleReceipts,omitempty"`
	LegacyRedemptionDate string               `json:"legacyRedemptionDate,omitempty"`
}
type Store struct{ control *controlstore.Store }

func Open(control *controlstore.Store) (*Store, error) {
	s := &Store{control: control}
	err := s.mutate(context.Background(), func(*state) error { return nil })
	return s, err
}
func active(s string) bool { return s == "queued" || s == "running" }
func decode(raw json.RawMessage) (state, error) {
	v := state{Jobs: []savedJob{}}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &v); err != nil {
			return v, err
		}
	}
	if len(v.Jobs) > 1000 {
		return v, errors.New("invalid jobs capacity")
	}
	now := time.Now()
	if v.ScheduleReceipts == nil {
		v.ScheduleReceipts = map[string]time.Time{}
	}
	for id, expiry := range v.ScheduleReceipts {
		if !expiry.After(now) {
			delete(v.ScheduleReceipts, id)
		}
	}
	for i := range v.Jobs {
		j := &v.Jobs[i]
		if j.State == "running" && !j.Until.After(now) {
			j.State = "interrupted"
			j.UpdatedAt = now
			for k := range j.Items {
				if j.Items[k].State == "queued" {
					j.Items[k].State = "cancelled"
				}
				if j.Items[k].State == "running" {
					j.Items[k].State = "interrupted"
					j.Items[k].Error = "owner_expired"
				}
			}
		}
	}
	return v, nil
}
func (s *Store) mutate(ctx context.Context, f func(*state) error) error {
	return s.control.Mutate(ctx, document, func(raw json.RawMessage) (any, error) {
		v, err := decode(raw)
		if err != nil {
			return nil, err
		}
		if err = f(&v); err != nil {
			return nil, err
		}
		return v, nil
	})
}
func (s *Store) List(ctx context.Context) ([]Job, error) {
	var raw json.RawMessage
	_, err := s.control.Load(ctx, document, &raw)
	if err != nil {
		return nil, err
	}
	var persisted state
	if err := json.Unmarshal(raw, &persisted); err != nil {
		return nil, err
	}
	for _, job := range persisted.Jobs {
		if job.State == "running" && !job.Until.After(time.Now()) {
			if err := s.mutate(ctx, func(*state) error { return nil }); err != nil {
				return nil, err
			}
			_, err = s.control.Load(ctx, document, &raw)
			if err != nil {
				return nil, err
			}
			break
		}
	}
	v, err := decode(raw)
	if err != nil {
		return nil, err
	}
	result := make([]Job, 0, len(v.Jobs))
	for i := len(v.Jobs) - 1; i >= 0; i-- {
		result = append(result, v.Jobs[i].Job)
	}
	return result, nil
}
func validKind(kind string) bool {
	return kind == "checkin" || kind == "redeem" || kind == "refresh" || kind == "probe"
}
func (s *Store) Submit(ctx context.Context, kind string, targets []string, params any) (Job, bool, error) {
	return s.submit(ctx, kind, targets, params, "", "")
}
func (s *Store) SubmitScheduled(ctx context.Context, kind string, targets []string, params any, date string) (Job, bool, error) {
	return s.SubmitScheduledRearmed(ctx, kind, targets, params, date, time.Time{})
}

// SubmitScheduledRearmed permits another daily attempt only for an explicitly
// saved future execution time. Code/target edits retain the daily receipt.
func (s *Store) SubmitScheduledRearmed(ctx context.Context, kind string, targets []string, params any, date string, rearm time.Time) (Job, bool, error) {
	if _, err := time.Parse("2006-01-02", date); err != nil {
		return Job{}, false, err
	}
	suffix := ""
	if !rearm.IsZero() {
		suffix = "/" + rearm.UTC().Format(time.RFC3339)
	}
	return s.submit(ctx, kind, targets, params, date, suffix)
}
func (s *Store) submit(ctx context.Context, kind string, targets []string, params any, date, scheduleSuffix string) (Job, bool, error) {
	if !validKind(kind) || len(targets) == 0 || len(targets) > 100 {
		return Job{}, false, errors.New("invalid job kind or targets")
	}
	targets = append([]string(nil), targets...)
	sort.Strings(targets)
	for i, id := range targets {
		if id == "" || len(id) > 128 || (i > 0 && id == targets[i-1]) {
			return Job{}, false, errors.New("invalid or duplicate target")
		}
	}
	encoded, err := json.Marshal(params)
	if err != nil {
		return Job{}, false, err
	}
	if len(encoded) > 4096 {
		return Job{}, false, errors.New("job parameters too large")
	}
	identity, _ := json.Marshal([]any{kind, targets, json.RawMessage(encoded)})
	digest := sha256.Sum256(identity)
	dedupe := hex.EncodeToString(digest[:])
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		return Job{}, false, err
	}
	now := time.Now().UTC()
	job := savedJob{Job: Job{ID: "job_" + hex.EncodeToString(random[:]), Kind: kind, State: "queued", CreatedAt: now, UpdatedAt: now}, Params: encoded, Dedupe: dedupe}
	if date != "" {
		job.Schedule = date + "/" + kind + scheduleSuffix
	}
	for _, id := range targets {
		job.Items = append(job.Items, Item{Target: id, State: "queued"})
	}
	duplicate := false
	err = s.mutate(ctx, func(v *state) error {
		if kind == "redeem" && date != "" && scheduleSuffix == "" && v.LegacyRedemptionDate == date {
			job.Job.State, duplicate = "interrupted", true
			for i := range job.Items {
				job.Items[i].State, job.Items[i].Error = "interrupted", "legacy_attempt_recorded"
			}
			return nil
		}
		if until, exists := v.ScheduleReceipts[job.Schedule]; job.Schedule != "" && exists && until.After(time.Now()) {
			for _, old := range v.Jobs {
				if old.Schedule == job.Schedule {
					job, duplicate = old, true
					return nil
				}
			}
			job.Job.State, duplicate = "interrupted", true
			for i := range job.Items {
				job.Items[i].State, job.Items[i].Error = "interrupted", "schedule_already_attempted"
			}
			return nil
		}
		pending := 0
		for _, old := range v.Jobs {
			if job.Schedule != "" && old.Schedule == job.Schedule {
				job = old
				duplicate = true
				return nil
			}
			if active(old.State) {
				pending++
				if old.Dedupe == dedupe {
					if job.Schedule != "" {
						v.ScheduleReceipts[job.Schedule] = now.Add(48 * time.Hour)
						v.ScheduleReceipts[date+"/"+kind] = now.Add(48 * time.Hour)
					}
					job = old
					duplicate = true
					return nil
				}
			}
		}
		if pending >= 32 {
			return ErrCapacity
		}
		if len(v.Jobs) >= 1000 {
			for i, old := range v.Jobs {
				if !active(old.State) {
					v.Jobs = append(v.Jobs[:i], v.Jobs[i+1:]...)
					break
				}
			}
		}
		v.Jobs = append(v.Jobs, job)
		if job.Schedule != "" {
			v.ScheduleReceipts[job.Schedule] = time.Now().Add(48 * time.Hour)
			v.ScheduleReceipts[date+"/"+kind] = time.Now().Add(48 * time.Hour)
		}
		return nil
	})
	return job.Job, duplicate, err
}

// ImportScheduler preserves the old once-per-day attempt marker. It deliberately
// does not replay an old redemption whose external result cannot be proven.
func (s *Store) ImportScheduler(ctx context.Context, path string) error {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var saved struct {
		LastTriggeredDate string `json:"lastTriggeredDate"`
	}
	if err := json.Unmarshal(data, &saved); err != nil {
		return err
	}
	if saved.LastTriggeredDate == "" {
		return nil
	}
	if _, err := time.Parse("2006-01-02", saved.LastTriggeredDate); err != nil {
		return err
	}
	if err := s.control.Import(ctx, "legacy-redemption-scheduler", path, saved); err != nil {
		return err
	}
	return s.mutate(ctx, func(v *state) error {
		if saved.LastTriggeredDate > v.LegacyRedemptionDate {
			v.LegacyRedemptionDate = saved.LastTriggeredDate
		}
		return nil
	})
}
func (s *Store) Cancel(ctx context.Context, id string) error {
	return s.mutate(ctx, func(v *state) error {
		for i := range v.Jobs {
			j := &v.Jobs[i]
			if j.ID != id {
				continue
			}
			if !active(j.State) {
				return nil
			}
			j.CancelRequested = true
			j.UpdatedAt = time.Now().UTC()
			if j.State == "queued" {
				j.State = "cancelled"
				for k := range j.Items {
					j.Items[k].State = "cancelled"
				}
			}
			return nil
		}
		return ErrMissing
	})
}
func (s *Store) Retry(ctx context.Context, id string) (Job, bool, error) {
	list, err := s.List(ctx)
	if err != nil {
		return Job{}, false, err
	}
	for _, job := range list {
		if job.ID != id {
			continue
		}
		if active(job.State) || job.Kind == "redeem" {
			return Job{}, false, ErrUnsafeRetry
		}
		targets := []string{}
		for _, item := range job.Items {
			if item.State != "succeeded" {
				targets = append(targets, item.Target)
			}
		}
		return s.Submit(ctx, job.Kind, targets, nil)
	}
	return Job{}, false, ErrMissing
}

type Executor func(context.Context, string, string, json.RawMessage) (any, error)
type Runner struct {
	store   *Store
	execute Executor
	owner   string
	cancel  context.CancelFunc
	wg      sync.WaitGroup
}

func Start(ctx context.Context, s *Store, execute Executor) *Runner {
	var id [16]byte
	_, err := rand.Read(id[:])
	if err != nil {
		panic(err)
	}
	ctx, cancel := context.WithCancel(ctx)
	r := &Runner{store: s, execute: execute, owner: hex.EncodeToString(id[:]), cancel: cancel}
	for i := 0; i < 2; i++ {
		r.wg.Add(1)
		go r.run(ctx)
	}
	return r
}
func (r *Runner) Close() { r.cancel(); r.wg.Wait() }
func (r *Runner) claim(ctx context.Context) (savedJob, bool, error) {
	var picked savedJob
	found := false
	list, err := r.store.List(ctx)
	if err != nil {
		return picked, false, err
	}
	queued := false
	for _, job := range list {
		if job.State == "queued" {
			queued = true
			break
		}
	}
	if !queued {
		return picked, false, nil
	}
	err = r.store.mutate(ctx, func(v *state) error {
		for i := range v.Jobs {
			j := &v.Jobs[i]
			if j.State != "queued" {
				continue
			}
			j.State = "running"
			j.Owner = r.owner
			j.Until = time.Now().Add(time.Minute)
			j.UpdatedAt = time.Now().UTC()
			picked = *j
			found = true
			break
		}
		return nil
	})
	return picked, found, err
}
func (r *Runner) owned(ctx context.Context, id string, f func(*savedJob) error) error {
	return r.store.mutate(ctx, func(v *state) error {
		for i := range v.Jobs {
			j := &v.Jobs[i]
			if j.ID != id {
				continue
			}
			if j.Owner != r.owner || j.State != "running" {
				return errors.New("job lease lost")
			}
			return f(j)
		}
		return ErrMissing
	})
}
func (r *Runner) run(ctx context.Context) {
	defer r.wg.Done()
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			job, ok, err := r.claim(ctx)
			if err == nil && ok {
				r.perform(ctx, job)
			}
		}
	}
}
func (r *Runner) perform(parent context.Context, job savedJob) {
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	stop := make(chan struct{})
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		ticker := time.NewTicker(500 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case <-ctx.Done():
				return
			case <-ticker.C:
				checkCtx, done := context.WithTimeout(ctx, 3*time.Second)
				var raw json.RawMessage
				_, err := r.store.control.Load(checkCtx, document, &raw)
				if err == nil {
					var v state
					v, err = decode(raw)
					if err == nil {
						err = ErrMissing
						for _, j := range v.Jobs {
							if j.ID == job.ID {
								if j.Owner != r.owner || j.State != "running" || j.CancelRequested {
									err = context.Canceled
								} else {
									err = nil
									if time.Until(j.Until) < 45*time.Second {
										err = r.owned(checkCtx, job.ID, func(j *savedJob) error { j.Until = time.Now().Add(time.Minute); return nil })
									}
								}
								break
							}
						}
					}
				}
				done()
				if err != nil {
					cancel()
					return
				}
			}
		}
	}()
	for index, item := range job.Items {
		if ctx.Err() != nil {
			break
		}
		err := r.owned(ctx, job.ID, func(j *savedJob) error {
			if j.CancelRequested {
				return context.Canceled
			}
			j.Items[index].State = "running"
			j.UpdatedAt = time.Now().UTC()
			return nil
		})
		if err != nil {
			cancel()
			break
		}
		itemCtx, done := context.WithTimeout(ctx, 2*time.Minute)
		result, execErr := r.execute(itemCtx, job.Kind, item.Target, job.Params)
		itemContextErr := itemCtx.Err()
		done()
		encoded, _ := json.Marshal(result)
		if len(encoded) > 8192 {
			encoded = nil
		}
		writeCtx, release := context.WithTimeout(context.Background(), 4*time.Second)
		err = r.owned(writeCtx, job.ID, func(j *savedJob) error {
			i := &j.Items[index]
			i.State = "succeeded"
			i.Result = encoded
			if execErr != nil {
				i.State = "failed"
				i.Error = "operation_failed"
				if itemContextErr != nil {
					i.State = "interrupted"
					i.Error = "result_unknown"
				}
			}
			j.UpdatedAt = time.Now().UTC()
			return nil
		})
		release()
		if err != nil {
			cancel()
			break
		}
	}
	close(stop)
	<-finished
	writeCtx, release := context.WithTimeout(context.Background(), 4*time.Second)
	defer release()
	_ = r.owned(writeCtx, job.ID, func(j *savedJob) error {
		success, failed := 0, 0
		for i := range j.Items {
			item := &j.Items[i]
			if item.State == "succeeded" {
				success++
			} else {
				failed++
				if item.State == "queued" {
					item.State, item.Error = "cancelled", "not_started"
				}
				if item.State == "running" {
					item.State = "interrupted"
					item.Error = "result_unknown"
				}
			}
		}
		j.State = "succeeded"
		if failed > 0 {
			j.State = "failed"
			if success > 0 {
				j.State = "partial"
			}
		}
		if ctx.Err() != nil {
			j.State = "interrupted"
		}
		if j.CancelRequested {
			j.State = "cancelled"
		}
		j.UpdatedAt = time.Now().UTC()
		j.Owner = ""
		j.Until = time.Time{}
		return nil
	})
}
