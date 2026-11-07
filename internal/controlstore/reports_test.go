package controlstore

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func TestCompactReportsSurviveDetailPruningAndMigrationStartsHonestWindow(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "control.sqlite3")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	payload := map[string]any{"model": "ÄMODEL", "accountId": "a", "apiKeyId": "k", "success": true, "durationMs": 123}
	event := AuditEvent{ID: "one", CreatedAt: now, Payload: payload, Finished: true, Count: true, Model: "ÄMODEL", Success: true, DurationMS: 123}
	if err := s.WriteAudit(ctx, []AuditEvent{event, event}, nil); err != nil {
		t.Fatal(err)
	}
	page, err := s.QueryAudit(ctx, AuditFilter{Model: "ämodel", PageSize: 20})
	if err != nil || page.Total != 1 {
		t.Fatalf("Unicode lookup: %+v %v", page, err)
	}
	if _, err := s.db.Exec("DELETE FROM audit_requests"); err != nil {
		t.Fatal(err)
	}
	events, started, err := s.ReportEvents(ctx, now.Add(-time.Hour), now.Add(time.Hour))
	if err != nil || len(events) != 1 || events[0].DurationMS != 123 || started.IsZero() {
		t.Fatalf("compact report: %+v %v", events, err)
	}
	if _, err := s.db.Exec("DROP TABLE audit_reports"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec("DROP TABLE report_meta"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec("PRAGMA user_version=2"); err != nil {
		t.Fatal(err)
	}
	s.Close()
	if err := VerifyFile(ctx, path); err != nil {
		t.Fatalf("legacy backup rejected: %v", err)
	}
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if migrated, since, err := s.ReportEvents(ctx, now.Add(-time.Hour), now.Add(time.Hour)); err != nil || len(migrated) != 0 || since.Before(now.Add(-time.Minute)) {
		t.Fatal(err)
	}
	files, _ := filepath.Glob(filepath.Join(filepath.Dir(path), "pre-migration", "schema-2-*.sqlite3"))
	if len(files) != 1 {
		t.Fatalf("migration snapshot missing: %v", files)
	}
}
