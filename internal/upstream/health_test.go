package upstream

import (
	"testing"
	"time"

	"clodesen2api/internal/config"
)

func TestRoutingHealthDoesNotProbeOrExcludeSaturatedAccounts(t *testing.T) {
	var missing *Upstream
	if missing.RoutingHealth().Total != 0 {
		t.Fatal("nil pool has accounts")
	}
	ready, paused, cooling := &Upstream{accountID: "a", enabled: true}, &Upstream{accountID: "b"}, &Upstream{accountID: "c", enabled: true}
	u := &Upstream{cfg: &config.Config{}, poolMode: true, accounts: []*Upstream{ready, paused, cooling}, rpmInFlight: map[string]int{"a": 4}, cooldownUntil: map[*Upstream]time.Time{cooling: time.Now().Add(time.Minute)}}
	got := u.RoutingHealth()
	if got.Total != 3 || got.Enabled != 2 || got.Available != 1 || got.Cooling != 1 || got.InFlight != 4 {
		t.Fatalf("health=%+v", got)
	}
	u.cooldownUntil[cooling] = time.Now().Add(-time.Minute)
	u.cooldownReason = map[*Upstream]string{cooling: cooldownReasonTemporaryBan}
	if u.RoutingHealth().Available != 2 {
		t.Fatal("expired cooldown remains unavailable")
	}
}
