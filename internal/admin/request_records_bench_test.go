package admin

import (
	"context"
	"fmt"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"clodesen2api/internal/controlstore"
	"clodesen2api/internal/usage"
)

func BenchmarkRequestRecords50000(b *testing.B) {
	dir := b.TempDir()
	db, err := controlstore.Open(filepath.Join(dir, "control.sqlite3"))
	if err != nil {
		b.Fatal(err)
	}
	defer db.Close()
	collector, err := usage.NewControl(db, "")
	if err != nil {
		b.Fatal(err)
	}
	defer collector.Close()
	events := make([]controlstore.AuditEvent, 50000)
	now := time.Now()
	for i := range events {
		record := usage.RequestRecord{RequestID: fmt.Sprintf("req_%d", i), CreatedAt: now.Add(-time.Duration(i) * time.Second), Model: "model", AccountID: "a", Account: "fixture", APIKeyID: fmt.Sprintf("key-%d", i%10), Type: "non_stream", Stage: "completed", Status: 200, Success: true, DurationMS: int64(i % 1000), Parameters: map[string]any{"messages": 4, "max_tokens": 2048}}
		events[i] = controlstore.AuditEvent{ID: record.RequestID, CreatedAt: record.CreatedAt, Payload: record, Finished: true}
	}
	if err := db.WriteAudit(context.Background(), events, nil); err != nil {
		b.Fatal(err)
	}
	s := &Service{usage: collector, control: db}
	request := httptest.NewRequest("GET", "/admin/api/request-records?key=key-5&page=2&pageSize=20", nil)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		w := httptest.NewRecorder()
		s.listRequestRecords(w, request)
		if w.Code != 200 {
			b.Fatalf("response=%d %s", w.Code, w.Body.String())
		}
	}
}
