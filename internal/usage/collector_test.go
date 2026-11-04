package usage

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
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

func TestPersistentCollectorRestoresCumulativeStats(t *testing.T) {
	path := filepath.Join(t.TempDir(), "usage.json")
	collector, err := NewPersistent(path)
	if err != nil {
		t.Fatalf("NewPersistent() error = %v", err)
	}
	collector.Record("gpt-5.6", 200, 800*time.Millisecond, nil)
	collector.Record("gpt-5.6", 500, 200*time.Millisecond, errors.New("failed"))

	restored, err := NewPersistent(path)
	if err != nil {
		t.Fatalf("restore error = %v", err)
	}
	snapshot := restored.Snapshot()
	if snapshot.TotalRequests != 2 || snapshot.Successes != 1 || snapshot.Failures != 1 {
		t.Fatalf("restored totals = %+v", snapshot)
	}
	if snapshot.AverageLatencyMS != 500 || len(snapshot.Models) != 1 || snapshot.Models[0].Requests != 2 {
		t.Fatalf("restored model stats = %+v", snapshot.Models)
	}
}

func TestPersistentCollectorMalformedFileStartsEmpty(t *testing.T) {
	path := filepath.Join(t.TempDir(), "usage.json")
	if err := os.WriteFile(path, []byte("not-json"), 0o600); err != nil {
		t.Fatal(err)
	}
	collector, err := NewPersistent(path)
	if err == nil {
		t.Fatal("NewPersistent() error = nil, want malformed file error")
	}
	if got := collector.Snapshot().TotalRequests; got != 0 {
		t.Fatalf("empty collector total = %d", got)
	}
}

func TestRequestRecordsPersistNewestFirstAndKeepSanitizedParameters(t *testing.T) {
	path := filepath.Join(t.TempDir(), "usage.json")
	collector, err := NewPersistent(path)
	if err != nil {
		t.Fatal(err)
	}
	first := int64(42)
	collector.RecordRequest(RequestRecord{
		CreatedAt: time.Now().Add(-time.Second), Model: "gpt-5.6", DurationMS: 120,
		FirstTokenMS: &first, Account: "one@example.test", Endpoint: "/v1/chat/completions",
		Type: "stream", Status: 200, Success: true, Parameters: map[string]any{"temperature": 0.2},
	})
	collector.RecordRequest(RequestRecord{Model: "gpt-image-2", Type: "image", Status: 502})

	records := collector.RequestRecords()
	if len(records) != 2 || records[0].Model != "gpt-image-2" || records[1].FirstTokenMS == nil || *records[1].FirstTokenMS != 42 {
		t.Fatalf("records = %+v", records)
	}
	restored, err := NewPersistent(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := restored.RequestRecords(); len(got) != 2 || got[1].Parameters["temperature"] != 0.2 {
		t.Fatalf("restored records = %+v", got)
	}
}

func TestRequestRecordPromptIsTrimmedAndRuneSafe(t *testing.T) {
	collector := New()
	prompt := "  " + strings.Repeat("图", maxRecordPromptRunes+32) + "  "
	collector.RecordRequest(RequestRecord{Model: "gpt-image-2", Type: "image", Prompt: prompt})

	records := collector.RequestRecords()
	if len(records) != 1 {
		t.Fatalf("records = %d, want 1", len(records))
	}
	if got := len([]rune(records[0].Prompt)); got != maxRecordPromptRunes {
		t.Fatalf("prompt rune length = %d, want %d", got, maxRecordPromptRunes)
	}
	if !utf8.ValidString(records[0].Prompt) {
		t.Fatal("prompt is not valid UTF-8 after truncation")
	}
}
