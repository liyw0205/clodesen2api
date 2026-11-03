package admin

import (
	"path/filepath"
	"testing"
)

func TestRedemptionHistoryPersistsAndReturnsNewestFirst(t *testing.T) {
	path := filepath.Join(t.TempDir(), "redemption-history.json")
	service := &Service{redemptionPath: path}
	service.appendRedemptionHistory(RedemptionHistoryRecord{ID: 1, CreatedAt: "2026-11-03T01:00:00Z", Code: "code-one", Account: "1***@qq.com", Status: "success"})
	service.appendRedemptionHistory(RedemptionHistoryRecord{ID: 2, CreatedAt: "2026-11-03T02:00:00Z", Code: "code-two", Account: "2***@qq.com", Status: "error"})

	reloaded := &Service{redemptionHistory: loadRedemptionHistory(path)}
	records := reloaded.redemptionHistorySnapshot()
	if len(records) != 2 || records[0].ID != 1 || records[1].Code != "code-two" {
		t.Fatalf("loaded redemption history = %+v", records)
	}
}
