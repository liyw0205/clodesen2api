package upstream

import (
	"context"
	"log"
	"time"

	"clodesen2api/internal/modelcooldown"
)

func (u *Upstream) SetModelCooldowns(store *modelcooldown.Store) {
	u.poolMu.Lock()
	u.modelCooldowns = store
	u.poolMu.Unlock()
}

func (u *Upstream) currentModelCooldowns() *modelcooldown.Store {
	owner := u.owner
	if owner == nil {
		owner = u
	}
	owner.poolMu.Lock()
	defer owner.poolMu.Unlock()
	return owner.modelCooldowns
}

func (u *Upstream) modelCooldownRemaining(ctx context.Context, model string) time.Duration {
	store := u.currentModelCooldowns()
	if store == nil || model == "" {
		return 0
	}
	return store.Remaining(ctx, u.accountID, model)
}

func (u *Upstream) recordModelCooldown(model, retryAfter string) {
	store := u.currentModelCooldowns()
	if store == nil {
		return
	}
	if err := store.Record(u.accountID, model, retryAfter); err != nil {
		log.Printf("[cooldown] model cooldown persistence failed; keeping local protection")
	}
}
