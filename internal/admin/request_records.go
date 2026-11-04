package admin

import (
	"net/http"
	"strings"
	"time"

	"clodesen2api/internal/usage"
)

func (s *Service) listRequestRecords(w http.ResponseWriter, r *http.Request) {
	records := s.usage.RequestRecords()
	records = filterRequestRecords(records, r)
	total := len(records)
	pageSize := queryPositiveInt(r, "pageSize", 20, 1, 100)
	page := queryPositiveInt(r, "page", 1, 1, 1000000)
	totalPages := (total + pageSize - 1) / pageSize
	if totalPages == 0 {
		totalPages = 1
	}
	if page > totalPages {
		page = totalPages
	}
	start := (page - 1) * pageSize
	if start > total {
		start = total
	}
	end := start + pageSize
	if end > total {
		end = total
	}
	pageRecords := records[start:end]
	writeJSON(w, http.StatusOK, map[string]any{
		"records": pageRecords,
		"total":   total, "page": page, "pageSize": pageSize, "totalPages": totalPages,
		"hasPrevious": page > 1, "hasNext": page < totalPages,
		"summary":     summarizeRequestRecords(records),
		"refreshedAt": time.Now(),
	})
}

func filterRequestRecords(records []usage.RequestRecord, r *http.Request) []usage.RequestRecord {
	model := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("model")))
	account := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("account")))
	typeName := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("type")))
	endpoint := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("endpoint")))
	if model == "" && account == "" && typeName == "" && endpoint == "" {
		return records
	}
	filtered := make([]usage.RequestRecord, 0, len(records))
	for _, record := range records {
		if model != "" && !strings.Contains(strings.ToLower(record.Model), model) {
			continue
		}
		if account != "" && !strings.Contains(strings.ToLower(record.Account), account) && !strings.Contains(strings.ToLower(record.AccountID), account) {
			continue
		}
		if typeName != "" && strings.ToLower(record.Type) != typeName {
			continue
		}
		if endpoint != "" && !strings.Contains(strings.ToLower(record.Endpoint), endpoint) {
			continue
		}
		filtered = append(filtered, record)
	}
	return filtered
}

type requestRecordSummary struct {
	Total           int            `json:"total"`
	Successes       int            `json:"successes"`
	Failures        int            `json:"failures"`
	AverageDuration int64          `json:"averageDurationMs"`
	AverageFirst    *int64         `json:"averageFirstTokenMs,omitempty"`
	Types           map[string]int `json:"types"`
}

func summarizeRequestRecords(records []usage.RequestRecord) requestRecordSummary {
	result := requestRecordSummary{Total: len(records), Types: make(map[string]int)}
	var duration int64
	var firstTotal int64
	var firstCount int64
	for _, record := range records {
		if record.Success {
			result.Successes++
		} else {
			result.Failures++
		}
		duration += record.DurationMS
		result.Types[record.Type]++
		if record.Type != "image" && record.FirstTokenMS != nil {
			firstTotal += *record.FirstTokenMS
			firstCount++
		}
	}
	if result.Total > 0 {
		result.AverageDuration = duration / int64(result.Total)
	}
	if firstCount > 0 {
		average := firstTotal / firstCount
		result.AverageFirst = &average
	}
	return result
}
