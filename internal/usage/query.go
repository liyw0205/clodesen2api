package usage

import (
	"context"
	"encoding/json"
	"time"

	"clodesen2api/internal/controlstore"
)

// QueryRecords returns nil for legacy in-memory collectors.
func (c *Collector) QueryRecords(ctx context.Context, filter controlstore.AuditFilter) (*controlstore.AuditPage, error) {
	if c == nil || c.writer == nil {
		return nil, nil
	}
	if err := c.Flush(ctx); err != nil {
		return nil, err
	}
	records, err := c.writer.store.UnfinishedAudit(ctx)
	if err != nil {
		return nil, err
	}
	if len(records) > 0 {
		var owners auditOwners
		if _, err := c.writer.store.Load(ctx, "audit-owners", &owners); err != nil {
			return nil, err
		}
		var recovered []controlstore.AuditEvent
		for _, raw := range records {
			var record RequestRecord
			if err := json.Unmarshal(raw, &record); err != nil {
				return nil, err
			}
			if record.Stage == "started" && !owners[record.Owner].After(time.Now()) {
				record.Stage = "interrupted"
				record.Success = false
				record.Error = "process interrupted before completion"
				event := auditEvent(record)
				event.RecoverOwner = record.Owner
				recovered = append(recovered, event)
			}
		}
		if len(recovered) > 0 {
			if err := c.writer.store.WriteAudit(ctx, recovered, nil); err != nil {
				return nil, err
			}
		}
	}
	result, err := c.writer.store.QueryAudit(ctx, filter)
	return &result, err
}
