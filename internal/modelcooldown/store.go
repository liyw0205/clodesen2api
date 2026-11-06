// Package modelcooldown keeps temporary upstream model refusals separate from
// account disablement and credit exhaustion.
package modelcooldown

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"strconv"
	"sync"
	"time"

	"clodesen2api/internal/controlstore"
)

const ceiling = 24 * time.Hour
const document = "model-cooldowns"

type Entry struct {
	AccountID   string    `json:"accountId"`
	Model       string    `json:"model"`
	Reason      string    `json:"reason"`
	LastError   string    `json:"lastError"`
	TriggeredAt time.Time `json:"triggeredAt"`
	Until       time.Time `json:"until"`
}

type Snapshot struct {
	Entries   []Entry `json:"entries"`
	Degraded  bool    `json:"degraded"`
	LastError string  `json:"lastError,omitempty"`
}

type Store struct {
	mu        sync.Mutex
	control   *controlstore.Store
	rows      map[string]Entry
	lastError string
}

func Open(control *controlstore.Store) (*Store, error) {
	s := &Store{control: control, rows: make(map[string]Entry)}
	if err := s.refresh(context.Background()); err != nil {
		return nil, err
	}
	return s, nil
}

func identity(account, model string) string {
	digest := sha256.Sum256([]byte(account + "\x00" + model))
	return hex.EncodeToString(digest[:])
}

func prune(rows map[string]Entry, now time.Time) {
	for id, row := range rows {
		if !row.Until.After(now) || row.Until.After(now.Add(ceiling)) || row.TriggeredAt.After(now.Add(time.Minute)) || len(row.AccountID) > 256 || len(row.Model) > 256 || row.Reason != "upstream_429" || id != identity(row.AccountID, row.Model) {
			delete(rows, id)
		}
	}
}

func (s *Store) refresh(ctx context.Context) error {
	var saved map[string]Entry
	_, err := s.control.Load(ctx, document, &saved)
	if errors.Is(err, controlstore.ErrMissing) {
		return nil
	}
	if err != nil {
		s.lastError = "cooldown_read_failed"
		return err
	}
	if len(saved) > 4096 {
		s.lastError = "cooldown_capacity_exceeded"
		return errors.New(s.lastError)
	}
	prune(saved, time.Now())
	for id, row := range s.rows {
		if row.Until.After(saved[id].Until) {
			saved[id] = row
		}
	}
	prune(saved, time.Now())
	s.rows = saved
	return nil
}

func RetryAfter(value string, now time.Time) time.Duration {
	duration := time.Minute
	if seconds, err := strconv.ParseInt(value, 10, 64); err == nil {
		if seconds >= int64(ceiling/time.Second) {
			return ceiling
		}
		if seconds > 0 {
			duration = time.Duration(seconds) * time.Second
		}
	} else if stamp, err := http.ParseTime(value); err == nil {
		duration = stamp.Sub(now)
	}
	if duration < time.Second {
		duration = time.Second
	}
	if duration > ceiling {
		duration = ceiling
	}
	return duration
}

func (s *Store) Record(account, model, retryAfter string) error {
	if len(account) > 256 || model == "" || len(model) > 256 {
		return errors.New("invalid cooldown identity")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now().UTC()
	row := Entry{AccountID: account, Model: model, Reason: "upstream_429", LastError: "HTTP 429", TriggeredAt: now, Until: now.Add(RetryAfter(retryAfter, now))}
	prune(s.rows, now)
	key := identity(account, model)
	if _, ok := s.rows[key]; !ok && len(s.rows) >= 4096 {
		s.lastError = "cooldown_capacity_exceeded"
		return errors.New(s.lastError)
	}
	s.rows[key] = row
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	err := s.control.Mutate(ctx, document, func(raw json.RawMessage) (any, error) {
		rows := make(map[string]Entry)
		if len(raw) > 0 {
			if err := json.Unmarshal(raw, &rows); err != nil {
				return nil, err
			}
		}
		prune(rows, now)
		for id, row := range s.rows {
			if row.Until.After(rows[id].Until) {
				rows[id] = row
			}
		}
		if len(rows) > 4096 {
			return nil, errors.New("cooldown capacity exceeded")
		}
		return rows, nil
	})
	if err != nil {
		s.lastError = "cooldown_write_failed"
	} else {
		s.lastError = ""
	}
	return err
}

func (s *Store) Remaining(ctx context.Context, account, model string) time.Duration {
	s.mu.Lock()
	defer s.mu.Unlock()
	_ = s.refresh(ctx)
	prune(s.rows, time.Now())
	remaining := time.Until(s.rows[identity(account, model)].Until)
	if remaining < 0 {
		return 0
	}
	return remaining
}

func (s *Store) Snapshot(ctx context.Context) Snapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	_ = s.refresh(ctx)
	prune(s.rows, time.Now())
	result := Snapshot{Entries: make([]Entry, 0, len(s.rows)), Degraded: s.lastError != "", LastError: s.lastError}
	for _, row := range s.rows {
		result.Entries = append(result.Entries, row)
	}
	sort.Slice(result.Entries, func(i, j int) bool {
		a, b := result.Entries[i], result.Entries[j]
		if a.AccountID == b.AccountID {
			return a.Model < b.Model
		}
		return a.AccountID < b.AccountID
	})
	return result
}
