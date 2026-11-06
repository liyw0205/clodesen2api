package admin

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"clodesen2api/internal/controlstore"
	"clodesen2api/internal/upstream"
)

func TestControlDataImportsOnceAndSharesWrites(t *testing.T) {
	dir := t.TempDir()
	ledgerPath := filepath.Join(dir, "credit-usage.json")
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if err := persistLocalCreditUsage(ledgerPath, []upstream.CreditUsageRecord{{ID: -1, CreatedAt: now}}); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(ledgerPath)
	db, err := controlstore.Open(filepath.Join(dir, "control.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	first := &Service{control: db, localCreditPath: ledgerPath}
	second := &Service{control: db, localCreditPath: ledgerPath}
	for _, service := range []*Service{first, second} {
		if err := service.MigrateControlData(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			service := first
			if i%2 != 0 {
				service = second
			}
			service.recordLocalCredit(upstream.CreditUsageRecord{ID: -int64(i + 2), CreatedAt: now})
			service.appendRedemptionHistory(RedemptionHistoryRecord{ID: int64(i + 1), CreatedAt: now, Status: "success"})
		}(i)
	}
	wg.Wait()
	if len(first.localCreditRecords()) != 21 || len(second.redemptionHistorySnapshot()) != 20 {
		t.Fatal("concurrent repository writes lost")
	}
	if err := second.MigrateControlData(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(first.localCreditRecords()) != 21 {
		t.Fatal("legacy source reimported")
	}
	after, _ := os.ReadFile(ledgerPath)
	if string(before) != string(after) {
		t.Fatal("legacy repository still written")
	}
}
