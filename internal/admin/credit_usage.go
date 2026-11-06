package admin

import (
	"context"
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"clodesen2api/internal/upstream"
)

const localCreditRetention = 7 * 24 * time.Hour

type localCreditUsageFile struct {
	Version int                          `json:"version"`
	Records []upstream.CreditUsageRecord `json:"records"`
}

func loadLocalCreditUsage(path string) []upstream.CreditUsageRecord {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var saved localCreditUsageFile
	if json.Unmarshal(data, &saved) != nil {
		return nil
	}
	return pruneCreditRecords(saved.Records, time.Now())
}

func (s *Service) recordLocalCredit(record upstream.CreditUsageRecord) {
	record.Source = "local"
	if record.ID >= 0 {
		record.ID = -time.Now().UnixNano()
	}
	if strings.TrimSpace(record.CreatedAt) == "" {
		record.CreatedAt = time.Now().UTC().Format(time.RFC3339Nano)
	}
	if s.control != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
		defer cancel()
		if err := s.control.Mutate(ctx, "credit-ledger", func(raw json.RawMessage) (any, error) {
			var value localCreditUsageFile
			if err := json.Unmarshal(raw, &value); err != nil {
				return nil, err
			}
			for _, old := range value.Records {
				if old.ID == record.ID {
					return value, nil
				}
			}
			value.Records = pruneCreditRecords(append(value.Records, record), time.Now())
			return value, nil
		}); err != nil {
			log.Printf("[storage] credit ledger write failed")
		}
		return
	}
	s.localCreditMu.Lock()
	s.localCreditUsage = append(pruneCreditRecords(s.localCreditUsage, time.Now()), record)
	s.localCreditUsage = pruneCreditRecords(s.localCreditUsage, time.Now())
	records := append([]upstream.CreditUsageRecord(nil), s.localCreditUsage...)
	_ = persistLocalCreditUsage(s.localCreditPath, records)
	s.localCreditMu.Unlock()
}

func (s *Service) localCreditRecords() []upstream.CreditUsageRecord {
	if s.control != nil {
		var saved localCreditUsageFile
		ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
		defer cancel()
		if _, err := s.control.Load(ctx, "credit-ledger", &saved); err == nil {
			return pruneCreditRecords(saved.Records, time.Now())
		}
		log.Printf("[storage] credit ledger read failed")
		return nil
	}
	s.localCreditMu.Lock()
	defer s.localCreditMu.Unlock()
	s.localCreditUsage = pruneCreditRecords(s.localCreditUsage, time.Now())
	return append([]upstream.CreditUsageRecord(nil), s.localCreditUsage...)
}

func persistLocalCreditUsage(path string, records []upstream.CreditUsageRecord) error {
	if strings.TrimSpace(path) == "" {
		return nil
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	file, err := os.CreateTemp(dir, ".credit-usage-*.tmp")
	if err != nil {
		return err
	}
	tempPath := file.Name()
	defer os.Remove(tempPath)
	if err := file.Chmod(0o600); err != nil {
		_ = file.Close()
		return err
	}
	if err := json.NewEncoder(file).Encode(localCreditUsageFile{Version: 1, Records: records}); err != nil {
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

func mergeCreditRecords(local, remote []upstream.CreditUsageRecord, now time.Time) []upstream.CreditUsageRecord {
	local = pruneCreditRecords(local, now)
	remote = pruneCreditRecords(remote, now)
	merged := make([]upstream.CreditUsageRecord, 0, len(local)+len(remote))
	merged = append(merged, remote...)
	for _, localRecord := range local {
		matched := false
		for _, remoteRecord := range remote {
			if remoteRecord.Type != "model_usage" || !sameCreditAccount(localRecord, remoteRecord) ||
				!sameCreditModel(localRecord, remoteRecord) || !nearCreditTime(localRecord, remoteRecord) {
				continue
			}
			if localRecord.Amount != 0 && remoteRecord.Amount != localRecord.Amount {
				continue
			}
			matched = true
			break
		}
		if !matched {
			merged = append(merged, localRecord)
		}
	}
	sort.SliceStable(merged, func(i, j int) bool {
		left, leftOK := parseCreditTime(merged[i].CreatedAt)
		right, rightOK := parseCreditTime(merged[j].CreatedAt)
		if leftOK && rightOK {
			return left.After(right)
		}
		return merged[i].CreatedAt > merged[j].CreatedAt
	})
	return merged
}

func pruneCreditRecords(records []upstream.CreditUsageRecord, now time.Time) []upstream.CreditUsageRecord {
	cutoff := now.Add(-localCreditRetention)
	kept := make([]upstream.CreditUsageRecord, 0, len(records))
	for _, record := range records {
		created, ok := parseCreditTime(record.CreatedAt)
		if ok && !created.Before(cutoff) {
			kept = append(kept, record)
		}
	}
	return kept
}

func parseCreditTime(value string) (time.Time, bool) {
	value = strings.TrimSpace(value)
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02 15:04:05", "2006-01-02T15:04:05"} {
		if created, err := time.Parse(layout, value); err == nil {
			return created, true
		}
	}
	return time.Time{}, false
}

func sameCreditAccount(left, right upstream.CreditUsageRecord) bool {
	if left.AccountID != "" && right.AccountID != "" {
		return left.AccountID == right.AccountID
	}
	return left.Identifier != "" && left.Identifier == right.Identifier
}

func sameCreditModel(left, right upstream.CreditUsageRecord) bool {
	return strings.EqualFold(strings.TrimSpace(left.Model), strings.TrimSpace(right.Model))
}

func nearCreditTime(left, right upstream.CreditUsageRecord) bool {
	leftTime, leftOK := parseCreditTime(left.CreatedAt)
	rightTime, rightOK := parseCreditTime(right.CreatedAt)
	return leftOK && rightOK && absDuration(leftTime.Sub(rightTime)) <= 10*time.Minute
}

func absDuration(value time.Duration) time.Duration {
	if value < 0 {
		return -value
	}
	return value
}
