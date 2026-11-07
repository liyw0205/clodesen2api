package admin

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"clodesen2api/internal/controlstore"
	"clodesen2api/internal/usage"
)

func TestDatabaseAuditMatchesLegacyFiltersSummaryAndPagination(t *testing.T) {
	db, err := controlstore.Open(filepath.Join(t.TempDir(), "control.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	collector, err := usage.NewControl(db, "")
	if err != nil {
		t.Fatal(err)
	}
	defer collector.Close()
	legacy := usage.New()
	now := time.Now()
	for i := 52; i >= 0; i-- {
		first := int64(i * 3)
		record := usage.RequestRecord{RequestID: fmt.Sprintf("req_%d", i), CreatedAt: now.Add(-time.Duration(i) * time.Minute), Model: "Model_%test", Account: "Fixture", AccountID: "a", APIKeyID: "key-a", Stage: "completed", Type: "non_stream", Success: i%3 == 0, Status: 200, DurationMS: int64(i * 11), FirstTokenMS: &first}
		if i%3 == 1 {
			record.Status = 502
		}
		if i%3 == 2 {
			record.AccountID = ""
			record.Account = ""
			record.Status = 403
			record.APIKeyID = "key-b"
		}
		if i%4 == 0 {
			record.Type = "image"
		}
		if i%4 == 1 {
			record.Type = "stream"
		}
		if i == 5 {
			record.Stage = "started"
			record.Success = false
		}
		if i == 6 {
			record.Stage = "interrupted"
			record.Success = false
		}
		collector.RecordRequest(record)
		legacy.RecordRequest(record)
	}
	for _, query := range []string{"", "?page=2&pageSize=20", "?page=999&pageSize=7", "?model=MoDel_%", "?account=fixture", "?account=A", "?type=image", "?type=stream", "?status=normal", "?status=abnormal", "?status=upstream_error", "?status=interrupted", "?status=running", "?key=key-b", "?model=%27%20OR%201%3D1--", "?key=key-a&type=non_stream&status=normal&pageSize=2"} {
		t.Run(query, func(t *testing.T) {
			var responses []map[string]any
			for _, source := range []*usage.Collector{legacy, collector} {
				s := &Service{usage: source}
				w := httptest.NewRecorder()
				s.listRequestRecords(w, httptest.NewRequest("GET", "/admin/api/request-records"+query, nil))
				if w.Code != 200 {
					t.Fatalf("status %d: %s", w.Code, w.Body.String())
				}
				var value map[string]any
				if err := json.Unmarshal(w.Body.Bytes(), &value); err != nil {
					t.Fatal(err)
				}
				delete(value, "refreshedAt")
				delete(value, "storage")
				for _, raw := range value["records"].([]any) {
					delete(raw.(map[string]any), "owner")
				}
				responses = append(responses, value)
			}
			for key, value := range responses[0] {
				if !reflect.DeepEqual(value, responses[1][key]) {
					t.Fatalf("query field %s differs\nlegacy=%v\nsql=%v", key, value, responses[1][key])
				}
			}
		})
	}
	if err := collector.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	(&Service{usage: collector}).listRequestRecords(w, httptest.NewRequest("GET", "/", nil))
	if w.Code != 503 {
		t.Fatalf("failed storage returned %d", w.Code)
	}
}
