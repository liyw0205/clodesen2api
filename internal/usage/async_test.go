package usage

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"clodesen2api/internal/controlstore"
)

func TestAsyncAuditLifecycleFlushAndRestart(t *testing.T) {
	dir := t.TempDir()
	store, err := controlstore.Open(filepath.Join(dir, "control.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	collector, err := NewControl(store, filepath.Join(dir, "usage.json"))
	if err != nil {
		t.Fatal(err)
	}
	start := RequestRecord{RequestID: "req_one", CreatedAt: time.Now(), Stage: "started", APIKeyID: "key-one", Model: "model", Endpoint: "/v1/chat/completions"}
	collector.RecordRequest(start)
	if collector.KeySummaries(time.Now().Add(-time.Hour))["key-one"].Requests != 0 {
		t.Fatal("running counted as failure")
	}
	finish := start
	finish.Stage = "completed"
	finish.Status = 200
	finish.Success = true
	finish.DurationMS = 123
	collector.Record("model", 200, 123*time.Millisecond, nil)
	collector.RecordRequest(finish)
	if len(collector.RequestRecords()) != 1 {
		t.Fatal("start and finish duplicated")
	}
	if err := collector.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := collector.Close(); err != nil {
		t.Fatal(err)
	}
	restored, err := NewControl(store, filepath.Join(dir, "usage.json"))
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	if got := restored.Snapshot(); got.TotalRequests != 1 || got.Successes != 1 || got.AverageLatencyMS != 123 {
		t.Fatalf("restored %+v", got)
	}
	if records := restored.RequestRecords(); len(records) != 1 || records[0].Stage != "completed" {
		t.Fatalf("records %+v", records)
	}
	// A repeated completion is idempotent in durable aggregates.
	restored.RecordRequest(finish)
	if err := restored.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	totals, err := store.AuditTotals(context.Background())
	if err != nil || len(totals) != 1 || totals[0].Requests != 1 {
		t.Fatalf("durable totals %+v %v", totals, err)
	}
}

func TestAsyncAuditStorageFailureIsBoundedAndVisible(t *testing.T) {
	dir := t.TempDir()
	store, err := controlstore.Open(filepath.Join(dir, "control.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	collector, err := NewControl(store, filepath.Join(dir, "usage.json"))
	if err != nil {
		t.Fatal(err)
	}
	store.Close()
	for i := 0; i < 2400; i++ {
		collector.RecordRequest(RequestRecord{RequestID: fmt.Sprintf("req_%d", i), Model: "test", Status: 200, Success: true})
	}
	if err := collector.Flush(context.Background()); err == nil {
		t.Fatal("failed storage reported success")
	}
	health := collector.StorageHealth()
	if !health.Degraded || health.Failures == 0 || health.LastFailure == nil {
		t.Fatalf("health %+v", health)
	}
	_ = collector.Close()
}

func TestStartingSecondCollectorDoesNotInterruptLiveRequests(t *testing.T) {
	dir := t.TempDir()
	store, err := controlstore.Open(filepath.Join(dir, "control.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	one, err := NewControl(store, "")
	if err != nil {
		t.Fatal(err)
	}
	defer one.Close()
	one.RecordRequest(RequestRecord{RequestID: "still-streaming", Stage: "started", CreatedAt: time.Now()})
	if err := one.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	two, err := NewControl(store, "")
	if err != nil {
		t.Fatal(err)
	}
	defer two.Close()
	if got := two.RequestRecords(); len(got) != 1 || got[0].Stage != "started" {
		t.Fatalf("second instance interrupted a live request: %+v", got)
	}
}

func TestAuditRecoversExpiredOwnerAndCloseIsIdempotent(t *testing.T) {
	store, err := controlstore.Open(filepath.Join(t.TempDir(), "control.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	one, err := NewControl(store, "")
	if err != nil {
		t.Fatal(err)
	}
	one.RecordRequest(RequestRecord{RequestID: "abandoned", Stage: "started", CreatedAt: time.Now(), Model: "model", Endpoint: "/v1/chat/completions"})
	if err := one.Close(); err != nil {
		t.Fatal(err)
	}
	if err := one.Close(); err != nil {
		t.Fatalf("second close: %v", err)
	}
	two, err := NewControl(store, "")
	if err != nil {
		t.Fatal(err)
	}
	defer two.Close()
	if got := two.RequestRecords(); len(got) != 1 || got[0].Stage != "interrupted" {
		t.Fatalf("recovery: %+v", got)
	}
	if got := two.Snapshot(); got.TotalRequests != 1 || got.Failures != 1 {
		t.Fatalf("totals: %+v", got)
	}
}

func TestAuditRefreshShowsSharedTotalsAndDetails(t *testing.T) {
	store, err := controlstore.Open(filepath.Join(t.TempDir(), "control.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	one, err := NewControl(store, "")
	if err != nil {
		t.Fatal(err)
	}
	defer one.Close()
	two, err := NewControl(store, "")
	if err != nil {
		t.Fatal(err)
	}
	defer two.Close()
	one.RecordRequest(RequestRecord{RequestID: "first", Model: "model", Status: 200, Success: true})
	two.RecordRequest(RequestRecord{RequestID: "second", Model: "model", Status: 500})
	if err := two.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := one.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := one.Snapshot(); got.TotalRequests != 2 || got.Successes != 1 || got.Failures != 1 {
		t.Fatalf("shared totals: %+v", got)
	}
	if got := one.RequestRecords(); len(got) != 2 {
		t.Fatalf("shared details: %+v", got)
	}
}
