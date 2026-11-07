package controlstore

import (
	"context"
	"database/sql"
	"time"
)

type ReportEvent struct {
	CreatedAt           time.Time
	Model, Account, Key string
	Success             bool
	DurationMS          int64
}

// Reporting retains only metrics, without prompts, IPs or credentials, for 31 days.
func (s *Store) ReportEvents(ctx context.Context, since, until time.Time) ([]ReportEvent, time.Time, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, time.Time{}, err
	}
	defer tx.Rollback()
	var started int64
	if err := tx.QueryRowContext(ctx, "SELECT since FROM report_meta WHERE id=1").Scan(&started); err != nil {
		return nil, time.Time{}, err
	}
	rows, err := tx.QueryContext(ctx, "SELECT created_at,model,account,key_id,success,duration_ms FROM audit_reports WHERE created_at>=? AND created_at<?", since.UnixNano(), until.UnixNano())
	if err != nil {
		return nil, time.Time{}, err
	}
	result := []ReportEvent{}
	for rows.Next() {
		var event ReportEvent
		var stamp int64
		if err := rows.Scan(&stamp, &event.Model, &event.Account, &event.Key, &event.Success, &event.DurationMS); err != nil {
			rows.Close()
			return nil, time.Time{}, err
		}
		event.CreatedAt = time.Unix(0, stamp)
		result = append(result, event)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, time.Time{}, err
	}
	return result, time.Unix(0, started), tx.Commit()
}
