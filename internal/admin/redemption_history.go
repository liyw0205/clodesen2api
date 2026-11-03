package admin

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"

	"clodesen2api/internal/upstream"
)

const maxRedemptionHistory = 500

type RedemptionHistoryRecord struct {
	ID        int64  `json:"id"`
	CreatedAt string `json:"createdAt"`
	Code      string `json:"code"`
	AccountID string `json:"accountId,omitempty"`
	Account   string `json:"account"`
	Status    string `json:"status"`
	Message   string `json:"message,omitempty"`
	Credits   *int64 `json:"credits,omitempty"`
}

type redemptionHistoryFile struct {
	Version int                       `json:"version"`
	Records []RedemptionHistoryRecord `json:"records"`
}

func (s *Service) recordRedemption(code string, result upstream.RedemptionResult) {
	s.appendRedemptionHistory(RedemptionHistoryRecord{
		CreatedAt: time.Now().UTC().Format(time.RFC3339Nano), Code: code,
		AccountID: result.AccountID, Account: maskIdentifier(result.Identifier),
		Status: result.Status, Message: result.Message, Credits: result.Credits,
	})
}

func loadRedemptionHistory(path string) []RedemptionHistoryRecord {
	if strings.TrimSpace(path) == "" {
		return nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var saved redemptionHistoryFile
	if json.Unmarshal(data, &saved) != nil {
		return nil
	}
	return trimRedemptionHistory(saved.Records)
}

func trimRedemptionHistory(records []RedemptionHistoryRecord) []RedemptionHistoryRecord {
	if len(records) <= maxRedemptionHistory {
		return append([]RedemptionHistoryRecord(nil), records...)
	}
	return append([]RedemptionHistoryRecord(nil), records[len(records)-maxRedemptionHistory:]...)
}

func (s *Service) appendRedemptionHistory(record RedemptionHistoryRecord) {
	if s == nil {
		return
	}
	if record.ID == 0 {
		record.ID = time.Now().UnixNano()
	}
	if strings.TrimSpace(record.CreatedAt) == "" {
		record.CreatedAt = time.Now().UTC().Format(time.RFC3339Nano)
	}
	s.redemptionMu.Lock()
	s.redemptionHistory = trimRedemptionHistory(append(s.redemptionHistory, record))
	if strings.TrimSpace(s.redemptionPath) != "" {
		_ = persistRedemptionHistory(s.redemptionPath, s.redemptionHistory)
	}
	s.redemptionMu.Unlock()
}

func (s *Service) redemptionHistorySnapshot() []RedemptionHistoryRecord {
	if s == nil {
		return nil
	}
	s.redemptionMu.Lock()
	defer s.redemptionMu.Unlock()
	return append([]RedemptionHistoryRecord(nil), s.redemptionHistory...)
}

func persistRedemptionHistory(path string, records []RedemptionHistoryRecord) error {
	if strings.TrimSpace(path) == "" {
		return nil
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	file, err := os.CreateTemp(dir, ".redemption-history-*.tmp")
	if err != nil {
		return err
	}
	tempPath := file.Name()
	defer os.Remove(tempPath)
	if err := file.Chmod(0o600); err != nil {
		_ = file.Close()
		return err
	}
	if err := json.NewEncoder(file).Encode(redemptionHistoryFile{Version: 1, Records: records}); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	return os.Rename(tempPath, path)
}
