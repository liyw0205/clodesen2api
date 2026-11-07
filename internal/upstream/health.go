package upstream

import "time"

type RoutingHealth struct {
	Total     int `json:"total"`
	Enabled   int `json:"enabled"`
	Available int `json:"available"`
	Cooling   int `json:"cooling"`
	InFlight  int `json:"inFlight"`
}

// RoutingHealth reports local routing state without logging in or probing an upstream.
func (u *Upstream) RoutingHealth() RoutingHealth {
	if u == nil {
		return RoutingHealth{}
	}
	u.poolMu.Lock()
	defer u.poolMu.Unlock()
	accounts := u.accounts
	direct := !u.poolMode && len(accounts) == 0 && u.cfg.Auth.Identifier != ""
	if direct {
		accounts = []*Upstream{u}
	}
	result := RoutingHealth{Total: len(accounts)}
	for _, account := range accounts {
		if !direct && !account.enabled {
			continue
		}
		result.Enabled++
		result.InFlight += u.rpmInFlight[account.accountID]
		if u.accountCoolingLocked(account, time.Now()) {
			result.Cooling++
		} else {
			result.Available++
		}
	}
	return result
}
