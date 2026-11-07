package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"sort"
	"strings"
	"time"

	"clodesen2api/internal/controlstore"
	"clodesen2api/internal/usage"
)

func (s *Service) listRequestRecords(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 6*time.Second)
	defer cancel()
	query := r.URL.Query()
	result, err := s.usage.QueryRecords(ctx, controlstore.AuditFilter{Model: query.Get("model"), Account: query.Get("account"), Type: query.Get("type"), Status: query.Get("status"), Key: strings.TrimSpace(query.Get("key")), Page: queryPositiveInt(r, "page", 1, 1, 1000000), PageSize: queryPositiveInt(r, "pageSize", 20, 1, 100)})
	if err != nil {
		s.usage.StorageFailure()
		writeError(w, http.StatusServiceUnavailable, "请求审计存储不可用，请稍后重试")
		return
	}
	if result != nil {
		records := make([]usage.RequestRecord, 0, len(result.Records))
		for _, raw := range result.Records {
			var record usage.RequestRecord
			if err := json.Unmarshal(raw, &record); err != nil {
				writeError(w, 503, "请求审计记录无效")
				return
			}
			record.StatusCategory = requestRecordCategory(record)
			records = append(records, record)
		}
		writeJSON(w, http.StatusOK, map[string]any{"records": records, "availableAccounts": result.Accounts, "total": result.Total, "page": result.Page, "pageSize": result.PageSize, "totalPages": result.TotalPages, "hasPrevious": result.Page > 1, "hasNext": result.Page < result.TotalPages, "summary": result.Summary, "storage": s.usage.StorageHealth(), "refreshedAt": time.Now()})
		return
	}
	s.refreshAudit(r)
	records := s.usage.RequestRecords()
	accounts := requestRecordAccounts(records)
	for index := range records {
		records[index].StatusCategory = requestRecordCategory(records[index])
	}
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
		"records":           pageRecords,
		"availableAccounts": accounts,
		"total":             total, "page": page, "pageSize": pageSize, "totalPages": totalPages,
		"hasPrevious": page > 1, "hasNext": page < totalPages,
		"summary":     summarizeRequestRecords(records),
		"storage":     s.usage.StorageHealth(),
		"refreshedAt": time.Now(),
	})
}

func (s *Service) refreshAudit(r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 6*time.Second)
	defer cancel()
	if err := s.usage.Refresh(ctx); err != nil {
		s.usage.StorageFailure()
	}
}

func filterRequestRecords(records []usage.RequestRecord, r *http.Request) []usage.RequestRecord {
	model := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("model")))
	account := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("account")))
	typeName := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("type")))
	status := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("status")))
	key := strings.TrimSpace(r.URL.Query().Get("key"))
	if model == "" && account == "" && typeName == "" && status == "" && key == "" {
		return records
	}
	filtered := make([]usage.RequestRecord, 0, len(records))
	for _, record := range records {
		if key != "" && record.APIKeyID != key {
			continue
		}
		if model != "" && !strings.Contains(strings.ToLower(record.Model), model) {
			continue
		}
		if account != "" && !strings.Contains(strings.ToLower(record.Account), account) && !strings.Contains(strings.ToLower(record.AccountID), account) {
			continue
		}
		if typeName != "" && strings.ToLower(record.Type) != typeName {
			continue
		}
		if status != "" && requestRecordCategory(record) != status {
			continue
		}
		filtered = append(filtered, record)
	}
	return filtered
}

type requestRecordAccount struct {
	ID    string `json:"id"`
	Label string `json:"label"`
}

func requestRecordAccounts(records []usage.RequestRecord) []requestRecordAccount {
	options := make(map[string]string)
	for _, record := range records {
		id := strings.TrimSpace(record.Account)
		label := id
		if id == "" {
			id = strings.TrimSpace(record.AccountID)
			label = id
		}
		if id != "" {
			options[id] = label
		}
	}
	ids := make([]string, 0, len(options))
	for id := range options {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	result := make([]requestRecordAccount, 0, len(ids))
	for _, id := range ids {
		result = append(result, requestRecordAccount{ID: id, Label: options[id]})
	}
	return result
}

func requestRecordCategory(record usage.RequestRecord) string {
	if record.Stage == "started" {
		return "running"
	}
	if record.Stage == "interrupted" {
		return "interrupted"
	}
	if record.Success {
		return "normal"
	}
	if strings.TrimSpace(record.AccountID) != "" && record.Status >= http.StatusBadRequest && record.Status < 600 {
		return "upstream_error"
	}
	return "abnormal"
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
		if record.Stage == "started" {
			continue
		}
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
	if finished := result.Successes + result.Failures; finished > 0 {
		result.AverageDuration = duration / int64(finished)
	}
	if firstCount > 0 {
		average := firstTotal / firstCount
		result.AverageFirst = &average
	}
	return result
}
