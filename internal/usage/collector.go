package usage

import (
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
	mu        sync.RWMutex
	startedAt time.Time
	total     counter
	byModel   map[string]*counter
}

func New() *Collector {
	return &Collector{startedAt: time.Now(), byModel: make(map[string]*counter)}
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

	c.mu.Lock()
	defer c.mu.Unlock()
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

func averageMS(duration time.Duration, count int64) int64 {
	if count == 0 {
		return 0
	}
	return int64(duration / time.Duration(count) / time.Millisecond)
}
