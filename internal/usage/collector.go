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
	persistPath string
}

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
	return collector, nil
}

func (c *Collector) Record(model string, status int, duration time.Duration, requestErr error) {
	if c == nil {
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
		Version:   1,
		StartedAt: c.startedAt,
		Total:     persistedFromCounter(c.total),
		Models:    make(map[string]persistedCounter, len(c.byModel)),
	}
	for model, item := range c.byModel {
		saved.Models[model] = persistedFromCounter(*item)
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
