package upstream

import (
	"context"
	"encoding/json"
	"errors"
)

// ProbeAccount verifies the selected account's login and model-list path.
// It does not send a billable inference request or switch to another account.
func (u *Upstream) ProbeAccount(ctx context.Context, id string) (int, error) {
	for _, account := range u.accountsSnapshot() {
		if account.accountID != id {
			continue
		}
		if !account.enabled {
			return 0, errors.New("account disabled")
		}
		body, err := account.ListModels(ctx)
		if err != nil {
			return 0, err
		}
		var payload struct {
			Data []json.RawMessage `json:"data"`
		}
		if err := json.Unmarshal(body, &payload); err != nil {
			return 0, err
		}
		return len(payload.Data), nil
	}
	return 0, errors.New("account missing")
}
