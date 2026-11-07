package admin

import (
	"context"
	"encoding/csv"
	"fmt"
	"math"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"clodesen2api/internal/usage"
)

type reportRow struct {
	Name        string  `json:"name"`
	Requests    int     `json:"requests"`
	Successes   int     `json:"successes"`
	Failures    int     `json:"failures"`
	SuccessRate float64 `json:"successRate"`
	AverageMS   int64   `json:"averageMs"`
	P95MS       int64   `json:"p95Ms"`
	latencies   []int64
}

type requestReport struct {
	Since         time.Time           `json:"since"`
	Until         time.Time           `json:"until"`
	Timezone      string              `json:"timezone"`
	Dimension     string              `json:"dimension"`
	RetentionDays int                 `json:"retentionDays"`
	RecordLimit   int                 `json:"recordLimit"`
	RetainedSince *time.Time          `json:"retainedSince"`
	Limited       bool                `json:"limited"`
	Coverage      string              `json:"coverage"`
	Rows          []reportRow         `json:"rows"`
	Storage       usage.StorageHealth `json:"storage"`
	Credits       []reportCredit      `json:"credits"`
	CreditsScope  string              `json:"creditsScope"`
}

type reportCredit struct {
	Name      string `json:"name"`
	Estimated int64  `json:"estimated"`
	Unknown   int    `json:"unknown"`
}

func buildReport(records []usage.RequestRecord, since, until time.Time, dimension string) requestReport {
	result := requestReport{Since: since, Until: until, Timezone: "Asia/Shanghai", Dimension: dimension, RetentionDays: 7, RecordLimit: 50000, Rows: []reportRow{}, Coverage: "retained_records"}
	result.Limited = since.Before(until.Add(-7*24*time.Hour)) || len(records) >= 50000
	groups := map[string]*reportRow{}
	for _, record := range records {
		if result.RetainedSince == nil || record.CreatedAt.Before(*result.RetainedSince) {
			stamp := record.CreatedAt
			result.RetainedSince = &stamp
		}
		if record.CreatedAt.Before(since) || !record.CreatedAt.Before(until) || record.Stage == "started" {
			continue
		}
		name := record.Model
		switch dimension {
		case "account":
			name = record.AccountID
			if name == "" {
				name = record.Account
			}
		case "key":
			name = record.APIKeyID
		case "day":
			name = record.CreatedAt.In(reportLocation()).Format("2006-01-02")
		}
		if name == "" {
			name = "(未分配)"
		}
		row := groups[name]
		if row == nil {
			row = &reportRow{Name: name}
			groups[name] = row
		}
		row.Requests++
		if record.Success {
			row.Successes++
		} else {
			row.Failures++
		}
		row.latencies = append(row.latencies, max(0, record.DurationMS))
		row.AverageMS += max(0, record.DurationMS)
	}
	for _, row := range groups {
		sort.Slice(row.latencies, func(i, j int) bool { return row.latencies[i] < row.latencies[j] })
		row.P95MS = row.latencies[int(math.Ceil(float64(row.Requests)*0.95))-1]
		row.AverageMS /= int64(row.Requests)
		row.SuccessRate = float64(row.Successes) * 100 / float64(row.Requests)
		row.latencies = nil
		result.Rows = append(result.Rows, *row)
	}
	sort.Slice(result.Rows, func(i, j int) bool { return result.Rows[i].Name < result.Rows[j].Name })
	return result
}

func reportLocation() *time.Location {
	return time.FixedZone("Asia/Shanghai", 8*60*60)
}

func (s *Service) reportsRoute(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", "GET")
		writeError(w, 405, "方法不支持")
		return
	}
	q := r.URL.Query()
	dimension := q.Get("dimension")
	if dimension == "" {
		dimension = "day"
	}
	if dimension != "day" && dimension != "model" && dimension != "account" && dimension != "key" {
		writeError(w, 400, "报表维度无效")
		return
	}
	now := time.Now()
	local := now.In(reportLocation())
	since := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, reportLocation())
	switch q.Get("range") {
	case "", "today":
	case "7d":
		since = since.AddDate(0, 0, -6)
	case "30d":
		since = since.AddDate(0, 0, -29)
	default:
		writeError(w, 400, "时间范围无效")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
	defer cancel()
	if err := s.usage.Refresh(ctx); err != nil {
		writeError(w, 503, "报表存储不可用")
		return
	}
	var result requestReport
	if s.control != nil {
		events, started, err := s.control.ReportEvents(ctx, since, now)
		if err != nil {
			writeError(w, 503, "报表存储不可用")
			return
		}
		records := make([]usage.RequestRecord, 0, len(events))
		for _, event := range events {
			records = append(records, usage.RequestRecord{CreatedAt: event.CreatedAt, Model: event.Model, AccountID: event.Account, APIKeyID: event.Key, Success: event.Success, DurationMS: event.DurationMS})
		}
		result = buildReport(records, since, now, dimension)
		result.Storage = s.usage.StorageHealth()
		result.RetentionDays = 31
		result.RecordLimit = 0
		result.RetainedSince = &started
		result.Limited = since.Before(started)
		result.Coverage = "compact_metrics_since_upgrade"
	} else {
		result = buildReport(s.usage.RequestRecords(), since, now, dimension)
		result.Storage = s.usage.StorageHealth()
	}
	result.Credits = []reportCredit{}
	result.CreditsScope = "local_estimates_7d_not_billing"
	if dimension == "key" {
		result.CreditsScope = "unavailable_no_key_attribution"
	} else {
		groups := map[string]*reportCredit{}
		for _, record := range s.localCreditRecords() {
			stamp, ok := parseCreditTime(record.CreatedAt)
			if !ok || stamp.Before(since) || !stamp.Before(now) {
				continue
			}
			name := record.Model
			if dimension == "account" {
				name = record.AccountID
			}
			if dimension == "day" {
				name = stamp.In(reportLocation()).Format("2006-01-02")
			}
			if name == "" {
				name = "(未分配)"
			}
			row := groups[name]
			if row == nil {
				row = &reportCredit{Name: name}
				groups[name] = row
			}
			row.Estimated -= record.Amount
			if record.Amount == 0 {
				row.Unknown++
			}
		}
		for _, row := range groups {
			result.Credits = append(result.Credits, *row)
		}
		sort.Slice(result.Credits, func(i, j int) bool { return result.Credits[i].Name < result.Credits[j].Name })
	}
	if q.Get("format") == "csv" {
		w.Header().Set("Content-Type", "text/csv; charset=utf-8")
		w.Header().Set("Content-Disposition", `attachment; filename="request-report.csv"`)
		writer := csv.NewWriter(w)
		_ = writer.Write([]string{"dimension", "name", "requests", "successes", "failures", "success_rate_percent", "average_ms", "p95_ms", "since", "until", "coverage", "limited", "local_estimated_credits_7d", "unpriced_requests", "credits_scope"})
		for _, row := range result.Rows {
			estimate, unknown := "", ""
			for _, credit := range result.Credits {
				if credit.Name == row.Name {
					estimate = strconv.FormatInt(credit.Estimated, 10)
					unknown = strconv.Itoa(credit.Unknown)
					break
				}
			}
			_ = writer.Write([]string{dimension, csvCell(row.Name), strconv.Itoa(row.Requests), strconv.Itoa(row.Successes), strconv.Itoa(row.Failures), fmt.Sprintf("%.2f", row.SuccessRate), strconv.FormatInt(row.AverageMS, 10), strconv.FormatInt(row.P95MS, 10), since.Format(time.RFC3339), now.Format(time.RFC3339), result.Coverage, strconv.FormatBool(result.Limited), estimate, unknown, result.CreditsScope})
		}
		writer.Flush()
		return
	}
	writeJSON(w, 200, result)
}

func csvCell(value string) string {
	trimmed := strings.TrimLeft(value, " \t\r\n")
	if (len(trimmed) > 0 && strings.ContainsRune("=+-@", rune(trimmed[0]))) || strings.HasPrefix(value, "\t") || strings.HasPrefix(value, "\r") {
		return "'" + value
	}
	return value
}
