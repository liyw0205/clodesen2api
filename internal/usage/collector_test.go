package usage

import (
	"errors"
	"testing"
	"time"
)

func TestCollectorAggregatesByModelAndSortsByRequestCount(t *testing.T) {
	collector := New()
	collector.Record("gpt-5.6", 200, 800*time.Millisecond, nil)
	collector.Record("gpt-5.6", 429, 200*time.Millisecond, nil)
	collector.Record("image-model", 502, time.Second, errors.New("upstream unavailable"))

	snapshot := collector.Snapshot()
	if snapshot.TotalRequests != 3 || snapshot.Successes != 1 || snapshot.Failures != 2 {
		t.Fatalf("totals = %+v", snapshot)
	}
	if snapshot.AverageLatencyMS != 666 {
		t.Fatalf("average latency = %dms, want 666ms", snapshot.AverageLatencyMS)
	}
	if len(snapshot.Models) != 2 || snapshot.Models[0].Model != "gpt-5.6" || snapshot.Models[0].Requests != 2 {
		t.Fatalf("models = %+v", snapshot.Models)
	}
	if snapshot.Models[0].Successes != 1 || snapshot.Models[0].Failures != 1 || snapshot.Models[0].AverageLatencyMS != 500 {
		t.Fatalf("model summary = %+v", snapshot.Models[0])
	}
}

func TestCollectorLabelsMissingModel(t *testing.T) {
	collector := New()
	collector.Record("  ", 200, 0, nil)
	if got := collector.Snapshot().Models[0].Model; got != "未指定" {
		t.Fatalf("model = %q, want 未指定", got)
	}
}
