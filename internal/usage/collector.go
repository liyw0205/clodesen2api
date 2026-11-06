package usage

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

type ModelSummary struct {
	Model            string    `json:"model"`
	Requests         int64     `json:"requests"`
	Successes        int64     `json:"successes"`
	Failures         int64     `json:"failures"`
	AverageLatencyMS int64     `json:"averageLatencyMs"`
	LastUsedAt       time.Time `json:"lastUsedAt"`
}

type Snapshot struct {
	StartedAt        time.Time      `json:"startedAt"`
	TotalRequests    int64          `json:"totalRequests"`
	Successes        int64          `json:"successes"`
	Failures         int64          `json:"failures"`
	AverageLatencyMS int64          `json:"averageLatencyMs"`
	Models           []ModelSummary `json:"models"`
	Storage          StorageHealth  `json:"storage"`
}

// RequestRecord is one client model request. Parameters are a deliberately
// sanitized summary; only image prompts are retained for panel inspection.
type RequestRecord struct {
	ID             int64          `json:"id"`
	CreatedAt      time.Time      `json:"createdAt"`
	Model          string         `json:"model"`
	SourceModel    string         `json:"sourceModel,omitempty"`
	UpstreamModel  string         `json:"upstreamModel,omitempty"`
	PolicyRevision int64          `json:"policyRevision,omitempty"`
	DurationMS     int64          `json:"durationMs"`
	FirstTokenMS   *int64         `json:"firstTokenMs,omitempty"`
	ThinkingLevel  string         `json:"thinkingLevel,omitempty"`
	Prompt         string         `json:"prompt,omitempty"`
	AccountID      string         `json:"accountId,omitempty"`
	Account        string         `json:"account,omitempty"`
	APIKeyID       string         `json:"apiKeyId,omitempty"`
	APIKeyName     string         `json:"apiKeyName,omitempty"`
	ClientIP       string         `json:"clientIP,omitempty"`
	AuthResult     string         `json:"authResult,omitempty"`
	AuthErrorCode  string         `json:"authErrorCode,omitempty"`
	RequestID      string         `json:"requestId,omitempty"`
	Stage          string         `json:"stage,omitempty"`
	Owner          string         `json:"owner,omitempty"`
	Endpoint       string         `json:"endpoint"`
	Type           string         `json:"type"`
	Status         int            `json:"status"`
	StatusCategory string         `json:"statusCategory,omitempty"`
	Success        bool           `json:"success"`
	Error          string         `json:"error,omitempty"`
	Parameters     map[string]any `json:"parameters,omitempty"`
	Attempts       []Attempt      `json:"attempts,omitempty"`
}

type Attempt struct {
	ID         string `json:"id"`
	AccountID  string `json:"accountId,omitempty"`
	Account    string `json:"account,omitempty"`
	Model      string `json:"model"`
	Status     int    `json:"status"`
	DurationMS int64  `json:"durationMs"`
	Wrote      bool   `json:"wrote"`
	ErrorCode  string `json:"errorCode,omitempty"`
}

type KeySummary struct {
	Requests          int64      `json:"requests"`
	Successes         int64      `json:"successes"`
	Failures          int64      `json:"failures"`
	AverageDurationMS int64      `json:"averageDurationMs"`
	LastUsedAt        *time.Time `json:"lastUsedAt,omitempty"`
}

// KeySummaries counts retained events in a fixed reporting interval.
func (c *Collector) KeySummaries(since time.Time) map[string]KeySummary {
	result := make(map[string]KeySummary)
	if c == nil {
		return result
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	totals := make(map[string]int64)
	for _, record := range c.requests {
		if record.Stage == "started" {
			continue
		}
		if record.APIKeyID == "" || record.CreatedAt.Before(since) {
			continue
		}
		item := result[record.APIKeyID]
		item.Requests++
		if record.Success {
			item.Successes++
		} else {
			item.Failures++
		}
		if item.LastUsedAt == nil || record.CreatedAt.After(*item.LastUsedAt) {
			stamp := record.CreatedAt
			item.LastUsedAt = &stamp
		}
		totals[record.APIKeyID] += record.DurationMS
		item.AverageDurationMS = totals[record.APIKeyID] / item.Requests
		result[record.APIKeyID] = item
	}
	return result
}

type counter struct {
	requests  int64
	successes int64
	failures  int64
	latency   time.Duration
	lastUsed  time.Time
}

type Collector struct {
	mu          sync.RWMutex
	persistMu   sync.Mutex
	startedAt   time.Time
	total       counter
	byModel     map[string]*counter
	requests    []RequestRecord
	persistPath string
	writer      *asyncWriter
	generation  uint64
}

const maxRequestRecords = 50000
const maxRecordPromptRunes = 4096

func New() *Collector {
	return &Collector{startedAt: time.Now(), byModel: make(map[string]*counter)}
}

type persistedCounter struct {
	Requests     int64     `json:"requests"`
	Successes    int64     `json:"successes"`
	Failures     int64     `json:"failures"`
	LatencyNanos int64     `json:"latencyNanos"`
	LastUsedAt   time.Time `json:"lastUsedAt"`
}

type persistedCollector struct {
	Version   int                         `json:"version"`
	StartedAt time.Time                   `json:"startedAt"`
	Total     persistedCounter            `json:"total"`
	Models    map[string]persistedCounter `json:"models"`
	Requests  []RequestRecord             `json:"requests,omitempty"`
}

// NewPersistent restores cumulative request statistics from path when possible.
// A missing file starts a fresh collector; malformed data is returned as an error
// while still returning a usable empty collector.
func NewPersistent(path string) (*Collector, error) {
	collector := New()
	collector.persistPath = strings.TrimSpace(path)
	if collector.persistPath == "" {
		return collector, nil
	}
	data, err := os.ReadFile(collector.persistPath)
	if errors.Is(err, os.ErrNotExist) {
		return collector, nil
	}
	if err != nil {
		return collector, err
	}
	var saved persistedCollector
	if err := json.Unmarshal(data, &saved); err != nil {
		return collector, err
	}
	if saved.Version != 1 && saved.Version != 2 {
		return collector, errors.New("unsupported usage file version")
	}
	if saved.StartedAt.IsZero() {
		saved.StartedAt = collector.startedAt
	}
	collector.startedAt = saved.StartedAt
	collector.total = counterFromPersisted(saved.Total)
	collector.byModel = make(map[string]*counter, len(saved.Models))
	for model, item := range saved.Models {
		if strings.TrimSpace(model) == "" {
			continue
		}
		value := counterFromPersisted(item)
		collector.byModel[model] = &value
	}
	collector.requests = pruneRequests(saved.Requests, time.Now())
	return collector, nil
}

// RecordRequest appends a request-level event and persists it with the
// aggregate counters. Events are retained for seven days.
func (c *Collector) RecordRequest(record RequestRecord) {
	if c == nil {
		return
	}
	now := time.Now()
	if record.CreatedAt.IsZero() {
		record.CreatedAt = now
	}
	if record.ID == 0 {
		record.ID = record.CreatedAt.UnixNano()
	}
	if record.Stage == "" {
		record.Stage = "completed"
	}
	if c.writer != nil {
		record.Owner = c.writer.owner
	}
	if record.DurationMS < 0 {
		record.DurationMS = 0
	}
	if record.FirstTokenMS != nil && *record.FirstTokenMS < 0 {
		value := int64(0)
		record.FirstTokenMS = &value
	}
	if len(record.Error) > 512 {
		record.Error = record.Error[:512]
	}
	record.Prompt = truncatePrompt(record.Prompt)
	record.Parameters = cloneParameters(record.Parameters)

	c.persistMu.Lock()
	defer c.persistMu.Unlock()
	c.mu.Lock()
	c.generation++
	replaced := false
	wasFinished := false
	if record.RequestID != "" {
		for i := len(c.requests) - 1; i >= 0; i-- {
			if c.requests[i].RequestID == record.RequestID {
				wasFinished = c.requests[i].Stage != "started"
				if wasFinished {
					c.mu.Unlock()
					return
				}
				c.requests[i] = record
				replaced = true
				break
			}
		}
	}
	if !replaced {
		c.requests = append(c.requests, record)
	}
	c.requests = pruneRequests(c.requests, now)
	if c.writer != nil {
		if record.Stage != "started" && record.Endpoint != "/v1/models" {
			model := record.Model
			if model == "" {
				model = "未指定"
			}
			item := c.byModel[model]
			if item == nil {
				item = &counter{}
				c.byModel[model] = item
			}
			for _, current := range []*counter{&c.total, item} {
				current.requests++
				current.latency += time.Duration(record.DurationMS) * time.Millisecond
				current.lastUsed = record.CreatedAt
				if record.Success {
					current.successes++
				} else {
					current.failures++
				}
			}
		}
		c.mu.Unlock()
		c.writer.enqueue(record)
		return
	}
	saved := c.persistedLocked()
	c.mu.Unlock()
	_ = persist(c.persistPath, saved)
}

func truncatePrompt(prompt string) string {
	prompt = strings.TrimSpace(prompt)
	if prompt == "" {
		return ""
	}
	runes := []rune(prompt)
	if len(runes) > maxRecordPromptRunes {
		runes = runes[:maxRecordPromptRunes]
	}
	return string(runes)
}

// RequestRecords returns newest-first request events retained by the collector.
func (c *Collector) RequestRecords() []RequestRecord {
	if c == nil {
		return nil
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	result := make([]RequestRecord, len(c.requests))
	for i, record := range c.requests {
		result[len(c.requests)-1-i] = cloneRequestRecord(record)
	}
	return result
}

func pruneRequests(records []RequestRecord, now time.Time) []RequestRecord {
	cutoff := now.Add(-7 * 24 * time.Hour)
	kept := records[:0]
	for _, record := range records {
		if record.CreatedAt.IsZero() || record.CreatedAt.After(cutoff) {
			kept = append(kept, record)
		}
	}
	if len(kept) > maxRequestRecords {
		kept = kept[len(kept)-maxRequestRecords:]
	}
	return kept
}

func cloneRequestRecord(record RequestRecord) RequestRecord {
	record.Parameters = cloneParameters(record.Parameters)
	record.Attempts = append([]Attempt(nil), record.Attempts...)
	if record.FirstTokenMS != nil {
		value := *record.FirstTokenMS
		record.FirstTokenMS = &value
	}
	return record
}

func cloneParameters(source map[string]any) map[string]any {
	if len(source) == 0 {
		return nil
	}
	result := make(map[string]any, len(source))
	for key, value := range source {
		result[key] = value
	}
	return result
}

func (c *Collector) Record(model string, status int, duration time.Duration, requestErr error) {
	if c == nil {
		return
	}
	if c.writer != nil {
		return
	}
	model = strings.TrimSpace(model)
	if model == "" {
		model = "未指定"
	}
	if duration < 0 {
		duration = 0
	}
	now := time.Now()
	success := requestErr == nil && status >= 200 && status < 400

	// Serialize persistence with updates so an older snapshot cannot overwrite
	// a newer one when concurrent requests finish close together.
	c.persistMu.Lock()
	defer c.persistMu.Unlock()
	c.mu.Lock()
	item := c.byModel[model]
	if item == nil {
		item = &counter{}
		c.byModel[model] = item
	}
	for _, current := range []*counter{&c.total, item} {
		current.requests++
		current.latency += duration
		current.lastUsed = now
		if success {
			current.successes++
		} else {
			current.failures++
		}
	}
	if c.writer != nil {
		c.mu.Unlock()
		return
	}
	saved := c.persistedLocked()
	c.mu.Unlock()
	_ = persist(c.persistPath, saved)
}

func (c *Collector) Snapshot() Snapshot {
	if c == nil {
		return Snapshot{}
	}
	c.mu.RLock()
	defer c.mu.RUnlock()

	snapshot := Snapshot{
		Storage:          c.StorageHealth(),
		StartedAt:        c.startedAt,
		TotalRequests:    c.total.requests,
		Successes:        c.total.successes,
		Failures:         c.total.failures,
		AverageLatencyMS: averageMS(c.total.latency, c.total.requests),
		Models:           make([]ModelSummary, 0, len(c.byModel)),
	}
	for model, item := range c.byModel {
		snapshot.Models = append(snapshot.Models, ModelSummary{
			Model:            model,
			Requests:         item.requests,
			Successes:        item.successes,
			Failures:         item.failures,
			AverageLatencyMS: averageMS(item.latency, item.requests),
			LastUsedAt:       item.lastUsed,
		})
	}
	sort.Slice(snapshot.Models, func(i, j int) bool {
		if snapshot.Models[i].Requests == snapshot.Models[j].Requests {
			return snapshot.Models[i].Model < snapshot.Models[j].Model
		}
		return snapshot.Models[i].Requests > snapshot.Models[j].Requests
	})
	return snapshot
}

func (c *Collector) persistedLocked() persistedCollector {
	saved := persistedCollector{
		Version:   2,
		StartedAt: c.startedAt,
		Total:     persistedFromCounter(c.total),
		Models:    make(map[string]persistedCounter, len(c.byModel)),
		Requests:  make([]RequestRecord, len(c.requests)),
	}
	for model, item := range c.byModel {
		saved.Models[model] = persistedFromCounter(*item)
	}
	for i, record := range c.requests {
		saved.Requests[i] = cloneRequestRecord(record)
	}
	return saved
}

func persistedFromCounter(item counter) persistedCounter {
	return persistedCounter{
		Requests: item.requests, Successes: item.successes, Failures: item.failures,
		LatencyNanos: item.latency.Nanoseconds(), LastUsedAt: item.lastUsed,
	}
}

func counterFromPersisted(item persistedCounter) counter {
	return counter{
		requests: item.Requests, successes: item.Successes, failures: item.Failures,
		latency: time.Duration(item.LatencyNanos), lastUsed: item.LastUsedAt,
	}
}

func persist(path string, saved persistedCollector) error {
	if strings.TrimSpace(path) == "" {
		return nil
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	file, err := os.CreateTemp(dir, ".usage-*.tmp")
	if err != nil {
		return err
	}
	tempPath := file.Name()
	defer os.Remove(tempPath)
	if err := file.Chmod(0o600); err != nil {
		_ = file.Close()
		return err
	}
	if err := json.NewEncoder(file).Encode(saved); err != nil {
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

func averageMS(duration time.Duration, count int64) int64 {
	if count == 0 {
		return 0
	}
	return int64(duration / time.Duration(count) / time.Millisecond)
}
