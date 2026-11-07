package controlstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

const auditCategory = `CASE WHEN json_extract(CAST(payload AS TEXT),'$.stage')='started' THEN 'running' WHEN json_extract(CAST(payload AS TEXT),'$.stage')='interrupted' THEN 'interrupted' WHEN json_extract(CAST(payload AS TEXT),'$.success')=1 THEN 'normal' WHEN TRIM(COALESCE(json_extract(CAST(payload AS TEXT),'$.accountId'),''))<>'' AND json_extract(CAST(payload AS TEXT),'$.status') BETWEEN 400 AND 599 THEN 'upstream_error' ELSE 'abnormal' END`
const auditAccount = `COALESCE(NULLIF(TRIM(json_extract(CAST(payload AS TEXT),'$.account')),''),TRIM(json_extract(CAST(payload AS TEXT),'$.accountId')),'')`

type AuditFilter struct {
	Model, Account, Type, Status, Key string
	Since, Until                      time.Time
	Page, PageSize                    int
}

type AuditSummary struct {
	Total           int            `json:"total"`
	Successes       int            `json:"successes"`
	Failures        int            `json:"failures"`
	AverageDuration int64          `json:"averageDurationMs"`
	AverageFirst    *int64         `json:"averageFirstTokenMs,omitempty"`
	Types           map[string]int `json:"types"`
}

type AuditAccount struct {
	ID    string `json:"id"`
	Label string `json:"label"`
}

type AuditPage struct {
	Records    []json.RawMessage `json:"records"`
	Accounts   []AuditAccount    `json:"availableAccounts"`
	Total      int               `json:"total"`
	Page       int               `json:"page"`
	PageSize   int               `json:"pageSize"`
	TotalPages int               `json:"totalPages"`
	Summary    AuditSummary      `json:"summary"`
}

func auditWhere(filter AuditFilter) (string, []any) {
	clauses := []string{"created_at>=?"}
	since := filter.Since
	if since.IsZero() {
		since = time.Now().Add(-7 * 24 * time.Hour)
	}
	args := []any{since.UnixNano()}
	if !filter.Until.IsZero() {
		clauses = append(clauses, "created_at<?")
		args = append(args, filter.Until.UnixNano())
	}
	for _, field := range []struct{ name, value string }{{"apiKeyId", filter.Key}, {"type", strings.ToLower(strings.TrimSpace(filter.Type))}} {
		if field.value != "" {
			clauses = append(clauses, "COALESCE(json_extract(CAST(payload AS TEXT),'$."+field.name+"'),'')=?")
			args = append(args, field.value)
		}
	}
	if filter.Status != "" {
		clauses = append(clauses, "("+auditCategory+")=?")
		args = append(args, strings.ToLower(strings.TrimSpace(filter.Status)))
	}
	if model := strings.ToLower(strings.TrimSpace(filter.Model)); model != "" {
		clauses = append(clauses, "instr(unicode_lower(COALESCE(json_extract(CAST(payload AS TEXT),'$.model'),'')),?)>0")
		args = append(args, model)
	}
	if account := strings.ToLower(strings.TrimSpace(filter.Account)); account != "" {
		clauses = append(clauses, "(instr(unicode_lower(COALESCE(json_extract(CAST(payload AS TEXT),'$.account'),'')),?)>0 OR instr(unicode_lower(COALESCE(json_extract(CAST(payload AS TEXT),'$.accountId'),'')),?)>0)")
		args = append(args, account, account)
	}
	return " WHERE " + strings.Join(clauses, " AND "), args
}

// QueryAudit reads filtered aggregates and a bounded detail page in one snapshot.
func (s *Store) QueryAudit(ctx context.Context, filter AuditFilter) (AuditPage, error) {
	result := AuditPage{Records: []json.RawMessage{}, Accounts: []AuditAccount{}, Page: max(1, filter.Page), PageSize: min(100, max(1, filter.PageSize)), Summary: AuditSummary{Types: map[string]int{}}}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return result, err
	}
	defer tx.Rollback()
	where, args := auditWhere(filter)
	rows, err := tx.QueryContext(ctx, `SELECT COALESCE(json_extract(CAST(payload AS TEXT),'$.type'),''),COALESCE(json_extract(CAST(payload AS TEXT),'$.stage'),''),COALESCE(json_extract(CAST(payload AS TEXT),'$.success'),0),COUNT(*),COALESCE(SUM(json_extract(CAST(payload AS TEXT),'$.durationMs')),0),COALESCE(SUM(CASE WHEN json_extract(CAST(payload AS TEXT),'$.type')<>'image' THEN json_extract(CAST(payload AS TEXT),'$.firstTokenMs') END),0),COUNT(CASE WHEN json_extract(CAST(payload AS TEXT),'$.type')<>'image' THEN json_extract(CAST(payload AS TEXT),'$.firstTokenMs') END) FROM audit_requests`+where+` GROUP BY 1,2,3`, args...)
	if err != nil {
		return result, err
	}
	var duration, first int64
	var firstCount int
	for rows.Next() {
		var kind, stage string
		var success, count, firstN int
		var elapsed, firstMS int64
		if err := rows.Scan(&kind, &stage, &success, &count, &elapsed, &firstMS, &firstN); err != nil {
			rows.Close()
			return result, err
		}
		result.Total += count
		if stage == "started" {
			continue
		}
		if success == 1 {
			result.Summary.Successes += count
		} else {
			result.Summary.Failures += count
		}
		result.Summary.Types[kind] += count
		duration += elapsed
		first += firstMS
		firstCount += firstN
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return result, err
	}
	rows.Close()
	result.Summary.Total = result.Total
	if count := result.Summary.Successes + result.Summary.Failures; count > 0 {
		result.Summary.AverageDuration = duration / int64(count)
	}
	if firstCount > 0 {
		average := first / int64(firstCount)
		result.Summary.AverageFirst = &average
	}
	result.TotalPages = max(1, (result.Total+result.PageSize-1)/result.PageSize)
	result.Page = min(result.Page, result.TotalPages)
	pageArgs := append(append([]any{}, args...), result.PageSize, (result.Page-1)*result.PageSize)
	rows, err = tx.QueryContext(ctx, `SELECT payload FROM audit_requests`+where+` ORDER BY created_at DESC,id DESC LIMIT ? OFFSET ?`, pageArgs...)
	if err != nil {
		return result, err
	}
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			rows.Close()
			return result, err
		}
		result.Records = append(result.Records, json.RawMessage(raw))
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return result, err
	}
	rows.Close()
	rows, err = tx.QueryContext(ctx, `SELECT DISTINCT `+auditAccount+` AS account FROM audit_requests INDEXED BY audit_account_created WHERE created_at>=? AND account<>'' ORDER BY account`, time.Now().Add(-7*24*time.Hour).UnixNano())
	if err != nil {
		return result, err
	}
	for rows.Next() {
		var item AuditAccount
		if err := rows.Scan(&item.ID); err != nil {
			rows.Close()
			return result, err
		}
		item.Label = item.ID
		result.Accounts = append(result.Accounts, item)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return result, err
	}
	rows.Close()
	return result, tx.Commit()
}

func (s *Store) UnfinishedAudit(ctx context.Context) ([]json.RawMessage, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT payload FROM audit_requests WHERE finished=0 AND created_at>=?", time.Now().Add(-7*24*time.Hour).UnixNano())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var records []json.RawMessage
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			return nil, fmt.Errorf("unfinished audit: %w", err)
		}
		records = append(records, json.RawMessage(raw))
	}
	return records, rows.Err()
}
