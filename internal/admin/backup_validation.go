package admin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"clodesen2api/internal/admission"
	"clodesen2api/internal/alerts"
	"clodesen2api/internal/apikey"
	"clodesen2api/internal/backups"
	"clodesen2api/internal/config"
	"clodesen2api/internal/controlstore"
	"clodesen2api/internal/jobs"
	"clodesen2api/internal/modelpolicy"
)

func ValidateControlBackup(ctx context.Context, dir string, base config.Config) error {
	var settings json.RawMessage
	seen := map[string]bool{}
	err := controlstore.InspectFile(ctx, filepath.Join(dir, "control.sqlite3"), func(name string, raw json.RawMessage) error {
		seen[name] = true
		var err error
		switch name {
		case alerts.Document:
			err = alerts.ValidateDocument(raw)
		case backups.ScheduleDocument:
			err = backups.ValidateScheduleDocument(raw)
		case loginSecurityDocument:
			_, err = decodeLoginState(raw)
		case "api-keys":
			err = apikey.ValidateDocument(raw)
		case "model-policies":
			err = modelpolicy.ValidateDocument(raw)
		case "inference-admission":
			err = admission.ValidateDocument(raw)
		case "background-jobs":
			err = jobs.ValidateDocument(raw)
		case "credit-ledger":
			var value localCreditUsageFile
			err = json.Unmarshal(raw, &value)
			if err == nil && value.Version != 1 {
				err = errors.New("invalid credit ledger schema")
			}
		case "redemption-history":
			var value redemptionHistoryFile
			err = json.Unmarshal(raw, &value)
			if err == nil && value.Version != 1 {
				err = errors.New("invalid redemption history schema")
			}
		case "admin-cache":
			var value persistentAdminCache
			err = json.Unmarshal(raw, &value)
			if err == nil && value.Version != persistentAdminCacheVersion {
				err = errors.New("invalid cache schema")
			}
		case "accounts":
			var value accountFile
			err = json.Unmarshal(raw, &value)
			if err == nil && (value.Version != 1 || config.StoredStrategy(value.Strategy) == "") {
				err = errors.New("invalid account schema")
			}
			if err == nil {
				err = validateCredentials(value.Accounts)
			}
		case "settings":
			settings = append(json.RawMessage(nil), raw...)
		case "sessions":
			var value map[string]savedSession
			err = json.Unmarshal(raw, &value)
			if err == nil && len(value) > 128 {
				err = errors.New("invalid sessions capacity")
			}
		}
		if err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
		return nil
	})
	if err != nil {
		return err
	}
	for _, name := range []string{"api-keys", "model-policies", "inference-admission", "accounts", "settings"} {
		if !seen[name] {
			return fmt.Errorf("missing control document %s", name)
		}
	}
	raw, err := os.ReadFile(filepath.Join(dir, "config.toml"))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	_, err = config.BackupConfig(base, raw, settings)
	return err
}
