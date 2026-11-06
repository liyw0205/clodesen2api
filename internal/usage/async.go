package usage

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strconv"
	"sync"
	"time"

	"clodesen2api/internal/controlstore"
)

type StorageHealth struct {
	Degraded    bool       `json:"degraded"`
	QueueLength int        `json:"queueLength"`
	Dropped     int64      `json:"dropped"`
	Failures    int64      `json:"failures"`
	LastSuccess *time.Time `json:"lastSuccess,omitempty"`
	LastFailure *time.Time `json:"lastFailure,omitempty"`
}

type asyncWriter struct {
	store     *controlstore.Store
	owner     string
	queue     chan RequestRecord
	flush     chan chan error
	stop      chan struct{}
	done      chan struct{}
	mu        sync.Mutex
	health    StorageHealth
	pending   int
	closing   bool
	closeErr  error
	closeOnce sync.Once
}

type auditOwners map[string]time.Time

func (w *asyncWriter) heartbeat(release bool) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return w.store.Mutate(ctx, "audit-owners", func(raw json.RawMessage) (any, error) {
		owners := make(auditOwners)
		if len(raw) > 0 {
			if err := json.Unmarshal(raw, &owners); err != nil {
				return nil, err
			}
		}
		now := time.Now()
		for id, expires := range owners {
			if !expires.After(now) {
				delete(owners, id)
			}
		}
		if release {
			delete(owners, w.owner)
		} else {
			owners[w.owner] = now.Add(time.Minute)
		}
		return owners, nil
	})
}

// NewControl imports legacy statistics and uses a bounded asynchronous writer.
func NewControl(store *controlstore.Store, legacy string) (*Collector, error) {
	ctx := context.Background()
	var saved persistedCollector
	_, err := store.Load(ctx, "usage", &saved)
	if errors.Is(err, controlstore.ErrMissing) {
		old, err := NewPersistent(legacy)
		if err != nil {
			return nil, err
		}
		old.mu.RLock()
		saved = old.persistedLocked()
		old.mu.RUnlock()
		initial := make([]controlstore.AuditEvent, 0, len(saved.Requests))
		for _, record := range saved.Requests {
			event := auditEvent(record)
			event.Count = false
			initial = append(initial, event)
		}
		saved.Requests = nil
		if err := store.ImportAudit(ctx, legacy, saved, initial); err != nil {
			return nil, err
		}
		if _, err := store.Load(ctx, "usage", &saved); err != nil {
			return nil, err
		}
	} else if err != nil {
		return nil, err
	}
	c := New()
	if !saved.StartedAt.IsZero() {
		c.startedAt = saved.StartedAt
	}
	c.total = counterFromPersisted(saved.Total)
	for model, item := range saved.Models {
		value := counterFromPersisted(item)
		c.byModel[model] = &value
	}
	rawOwner := make([]byte, 16)
	if _, err := rand.Read(rawOwner); err != nil {
		return nil, err
	}
	w := &asyncWriter{store: store, owner: hex.EncodeToString(rawOwner), queue: make(chan RequestRecord, 1024), flush: make(chan chan error), stop: make(chan struct{}), done: make(chan struct{})}
	if err := w.heartbeat(false); err != nil {
		return nil, err
	}
	var owners auditOwners
	if _, err := store.Load(ctx, "audit-owners", &owners); err != nil {
		return nil, err
	}
	records, err := store.AuditRecords(ctx)
	if err != nil {
		return nil, err
	}
	var recovered []controlstore.AuditEvent
	for _, raw := range records {
		var record RequestRecord
		if err := json.Unmarshal(raw, &record); err != nil {
			return nil, err
		}
		if record.Stage == "started" && !owners[record.Owner].After(time.Now()) {
			record.Stage = "interrupted"
			record.Success = false
			record.Error = "process interrupted before completion"
			event := auditEvent(record)
			event.RecoverOwner = record.Owner
			recovered = append(recovered, event)
		}
		c.requests = append(c.requests, record)
	}
	c.requests = pruneRequests(c.requests, time.Now())
	if len(recovered) > 0 {
		if err := store.WriteAudit(ctx, recovered, nil); err != nil {
			return nil, err
		}
		records, err = store.AuditRecords(ctx)
		if err != nil {
			return nil, err
		}
		c.requests = nil
		for _, raw := range records {
			var record RequestRecord
			if err := json.Unmarshal(raw, &record); err != nil {
				return nil, err
			}
			c.requests = append(c.requests, record)
		}
	}
	totals, err := store.AuditTotals(ctx)
	if err != nil {
		return nil, err
	}
	for _, total := range totals {
		item := c.byModel[total.Model]
		if item == nil {
			item = &counter{}
			c.byModel[total.Model] = item
		}
		for _, current := range []*counter{&c.total, item} {
			current.requests += total.Requests
			current.successes += total.Successes
			current.failures += total.Failures
			current.latency += time.Duration(total.LatencyMS) * time.Millisecond
		}
	}
	c.writer = w
	go w.run()
	return c, nil
}

func auditEvent(record RequestRecord) controlstore.AuditEvent {
	id := record.RequestID
	if id == "" {
		id = strconv.FormatInt(record.ID, 10)
	}
	model := record.Model
	if model == "" {
		model = "未指定"
	}
	return controlstore.AuditEvent{ID: id, CreatedAt: record.CreatedAt, Payload: record, Finished: record.Stage != "started", Count: record.Stage != "started" && record.Endpoint != "/v1/models", Model: model, Success: record.Success, DurationMS: record.DurationMS}
}

func (w *asyncWriter) enqueue(record RequestRecord) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closing {
		w.health.Dropped++
		w.health.Degraded = true
		return
	}
	select {
	case w.queue <- record:
	default:
		w.health.Dropped++
		w.health.Degraded = true
	}
}

func (w *asyncWriter) run() {
	defer close(w.done)
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	heartbeat := time.NewTicker(10 * time.Second)
	defer heartbeat.Stop()
	pending := make([]controlstore.AuditEvent, 0, 256)
	commit := func() error {
		for len(pending) < 1024 {
			select {
			case record := <-w.queue:
				pending = append(pending, auditEvent(record))
			default:
				goto drained
			}
		}
	drained:
		w.mu.Lock()
		w.pending = len(pending)
		w.mu.Unlock()
		if len(pending) == 0 {
			return nil
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		err := w.store.WriteAudit(ctx, pending, nil)
		cancel()
		w.mu.Lock()
		stamp := time.Now().UTC()
		if err != nil {
			w.health.Failures++
			w.health.LastFailure = &stamp
			w.health.Degraded = true
		} else {
			pending = pending[:0]
			w.pending = 0
			w.health.LastSuccess = &stamp
			w.health.Degraded = w.health.Dropped > 0
		}
		w.mu.Unlock()
		return err
	}
	drain := func() error {
		for {
			if err := commit(); err != nil {
				return err
			}
			if len(w.queue) == 0 {
				return nil
			}
		}
	}
	for {
		select {
		case record := <-w.queue:
			if len(pending) < 1024 {
				pending = append(pending, auditEvent(record))
				w.mu.Lock()
				w.pending = len(pending)
				w.mu.Unlock()
			} else {
				w.mu.Lock()
				w.health.Dropped++
				w.health.Degraded = true
				w.mu.Unlock()
			}
			if len(pending) >= 256 {
				_ = commit()
			}
		case <-ticker.C:
			_ = commit()
		case <-heartbeat.C:
			if err := w.heartbeat(false); err != nil {
				w.mu.Lock()
				w.health.Degraded = true
				w.health.Failures++
				stamp := time.Now().UTC()
				w.health.LastFailure = &stamp
				w.mu.Unlock()
			}
		case result := <-w.flush:
			result <- drain()
		case <-w.stop:
			err := drain()
			releaseErr := w.heartbeat(true)
			if err == nil {
				err = releaseErr
			}
			w.mu.Lock()
			w.closeErr = err
			w.mu.Unlock()
			return
		}
	}
}

func (c *Collector) StorageHealth() StorageHealth {
	if c == nil || c.writer == nil {
		return StorageHealth{}
	}
	w := c.writer
	w.mu.Lock()
	defer w.mu.Unlock()
	health := w.health
	health.QueueLength = len(w.queue) + w.pending
	return health
}

func (c *Collector) Flush(ctx context.Context) error {
	if c == nil || c.writer == nil {
		return nil
	}
	result := make(chan error, 1)
	select {
	case c.writer.flush <- result:
	case <-ctx.Done():
		return ctx.Err()
	case <-c.writer.done:
		return errors.New("audit writer is closed")
	}
	select {
	case err := <-result:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (c *Collector) StorageFailure() {
	if c == nil || c.writer == nil {
		return
	}
	w := c.writer
	w.mu.Lock()
	defer w.mu.Unlock()
	w.health.Degraded = true
	w.health.Failures++
	stamp := time.Now().UTC()
	w.health.LastFailure = &stamp
}

// Refresh publishes shared committed state if local events did not change
// during the read. Storage I/O never holds the inference mutation lock.
func (c *Collector) Refresh(ctx context.Context) error {
	if c == nil || c.writer == nil {
		return nil
	}
	c.mu.RLock()
	generation := c.generation
	c.mu.RUnlock()
	if err := c.Flush(ctx); err != nil {
		return err
	}
	var saved persistedCollector
	totals, records, err := c.writer.store.ReadAudit(ctx, &saved)
	if err != nil {
		return err
	}
	var owners auditOwners
	if _, err := c.writer.store.Load(ctx, "audit-owners", &owners); err != nil {
		return err
	}
	var recovered []controlstore.AuditEvent
	for _, raw := range records {
		var record RequestRecord
		if err := json.Unmarshal(raw, &record); err != nil {
			return err
		}
		if record.Stage == "started" && !owners[record.Owner].After(time.Now()) {
			record.Stage = "interrupted"
			record.Success = false
			record.Error = "process interrupted before completion"
			event := auditEvent(record)
			event.RecoverOwner = record.Owner
			recovered = append(recovered, event)
		}
	}
	if len(recovered) > 0 {
		if err := c.writer.store.WriteAudit(ctx, recovered, nil); err != nil {
			return err
		}
		totals, records, err = c.writer.store.ReadAudit(ctx, &saved)
		if err != nil {
			return err
		}
	}
	total := counterFromPersisted(saved.Total)
	models := make(map[string]*counter, len(saved.Models))
	for name, value := range saved.Models {
		item := counterFromPersisted(value)
		models[name] = &item
	}
	for _, row := range totals {
		item := models[row.Model]
		if item == nil {
			item = &counter{}
			models[row.Model] = item
		}
		for _, current := range []*counter{&total, item} {
			current.requests += row.Requests
			current.successes += row.Successes
			current.failures += row.Failures
			current.latency += time.Duration(row.LatencyMS) * time.Millisecond
		}
	}
	next := make([]RequestRecord, 0, len(records))
	for _, raw := range records {
		var record RequestRecord
		if err := json.Unmarshal(raw, &record); err != nil {
			return err
		}
		next = append(next, record)
		if item := models[record.Model]; item != nil && record.CreatedAt.After(item.lastUsed) {
			item.lastUsed = record.CreatedAt
		}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if generation != c.generation {
		return nil
	}
	c.total = total
	c.byModel = models
	c.requests = pruneRequests(next, time.Now())
	return nil
}

func (c *Collector) Close() error {
	if c == nil || c.writer == nil {
		return nil
	}
	w := c.writer
	w.closeOnce.Do(func() {
		w.mu.Lock()
		w.closing = true
		w.mu.Unlock()
		close(w.stop)
	})
	select {
	case <-w.done:
		w.mu.Lock()
		defer w.mu.Unlock()
		return w.closeErr
	}
}
