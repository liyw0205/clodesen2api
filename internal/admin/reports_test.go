package admin

import (
	"testing"
	"time"

	"clodesen2api/internal/usage"
)

func TestReportBoundariesPercentileAndCSV(t *testing.T) {
	since := time.Date(2026, 11, 8, 0, 0, 0, 0, reportLocation())
	until := since.Add(time.Hour)
	records := []usage.RequestRecord{{CreatedAt: since.Add(-time.Nanosecond), Model: "excluded"}, {CreatedAt: until, Model: "excluded"}, {CreatedAt: since, Stage: "started", Model: "excluded"}}
	for i := 1; i <= 20; i++ {
		records = append(records, usage.RequestRecord{CreatedAt: since.Add(time.Duration(i) * time.Second), Model: "m", AccountID: "a", APIKeyID: "k", Success: i%2 == 0, DurationMS: int64(i)})
	}
	for _, dimension := range []string{"day", "model", "account", "key"} {
		report := buildReport(records, since, until, dimension)
		if len(report.Rows) != 1 {
			t.Fatalf("unexpected groups: %+v", report.Rows)
		}
		row := report.Rows[0]
		if row.Requests != 20 || row.Successes != 10 || row.SuccessRate != 50 || row.P95MS != 19 || row.AverageMS != 10 {
			t.Fatalf("invalid report: %+v", row)
		}
	}
	for _, value := range []string{"=SUM(1)", " +1", "@formula", "\tsecret", "-cmd"} {
		if csvCell(value) != "'"+value {
			t.Fatalf("unsafe CSV %q", value)
		}
	}
	if csvCell("normal") != "normal" || csvCell("") != "" {
		t.Fatal("CSV text changed")
	}
}
