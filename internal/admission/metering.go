package admission

import (
	"context"
	"encoding/json"
	"errors"
	"time"
)

// Meter identifies an operator-verified upstream contract. Bounds cover the
// complete client request, including safe retries, in integer billing units.
type Meter struct {
	Source  string `json:"source"`
	Tokens  int64  `json:"tokenUpperBound"`
	Credits int64  `json:"creditUpperBound"`
}

func (m Meter) Validate() error {
	if m.Source != "" && m.Source != "upstream-usage-v1" {
		return errors.New("unsupported metering contract")
	}
	if m.Tokens < 0 || m.Credits < 0 || m.Tokens > 1_000_000_000_000 || m.Credits > 1_000_000_000_000 {
		return errors.New("invalid metering bound")
	}
	if m.Source == "" && (m.Tokens > 0 || m.Credits > 0) {
		return errors.New("metering source required")
	}
	return nil
}

type Amount struct {
	Tokens       int64 `json:"tokens"`
	Credits      int64 `json:"credits"`
	TokensKnown  bool  `json:"tokensKnown"`
	CreditsKnown bool  `json:"creditsKnown"`
}

type Reservation struct {
	ID        string    `json:"id"`
	Key       string    `json:"key"`
	Lease     string    `json:"lease"`
	Source    string    `json:"source"`
	State     string    `json:"state"`
	Version   int64     `json:"version"`
	Bound     Amount    `json:"bound"`
	Actual    Amount    `json:"actual"`
	Periods   []string  `json:"periods"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

type Balance struct {
	UsedTokens      int64     `json:"usedTokens"`
	ReservedTokens  int64     `json:"reservedTokens"`
	UsedCredits     int64     `json:"usedCredits"`
	ReservedCredits int64     `json:"reservedCredits"`
	Until           time.Time `json:"until"`
}

func (l *Lease) Metered() bool { l.mu.Lock(); defer l.mu.Unlock(); return l.reservation != "" }

func (l *Lease) reserve(v *state, current lease, p Policy, periods []period, meter Meter, requestID string) error {
	needTokens := p.DailyTokens > 0 || p.MonthlyTokens > 0
	needCredits := p.DailyCredits > 0 || p.MonthlyCredits > 0
	if !needTokens && !needCredits {
		return nil
	}
	if err := meter.Validate(); err != nil {
		return err
	}
	if requestID == "" || meter.Source == "" || (needTokens && meter.Tokens == 0) || (needCredits && meter.Credits == 0) {
		return &Error{403, "quota_metering_unavailable", 0}
	}
	if _, exists := v.Reservations[requestID]; exists {
		return errors.New("duplicate metering request ID")
	}
	if len(v.Reservations) >= 5000 {
		return &Error{503, "metering_capacity_exceeded", 3}
	}
	bound := Amount{Tokens: meter.Tokens, Credits: meter.Credits, TokensKnown: needTokens, CreditsKnown: needCredits}
	if !needTokens {
		bound.Tokens = 0
	}
	if !needCredits {
		bound.Credits = 0
	}
	for _, period := range periods {
		b := v.Balances[period.id]
		tokens, credits := p.DailyTokens, p.DailyCredits
		if period.monthly {
			tokens, credits = p.MonthlyTokens, p.MonthlyCredits
		}
		if (tokens > 0 && b.UsedTokens+b.ReservedTokens+bound.Tokens > tokens) || (credits > 0 && b.UsedCredits+b.ReservedCredits+bound.Credits > credits) {
			return &Error{429, "usage_quota_exceeded", int(time.Until(period.until).Seconds()) + 1}
		}
	}
	for _, old := range v.Reservations {
		if old.Key == current.Key && ((old.Bound.TokensKnown && old.Actual.TokensKnown && old.Actual.Tokens > old.Bound.Tokens) || (old.Bound.CreditsKnown && old.Actual.CreditsKnown && old.Actual.Credits > old.Bound.Credits)) {
			for _, id := range old.Periods {
				if _, active := v.Balances[id]; active {
					return &Error{403, "metering_contract_exceeded", 0}
				}
			}
		}
	}
	now := time.Now().UTC()
	r := Reservation{ID: requestID, Key: current.Key, Lease: l.id, Source: meter.Source, State: "reserved", Version: 1, Bound: bound, CreatedAt: now, UpdatedAt: now}
	for _, period := range periods {
		b := v.Balances[period.id]
		b.Until = period.until
		b.ReservedTokens += bound.Tokens
		b.ReservedCredits += bound.Credits
		v.Balances[period.id] = b
		r.Periods = append(r.Periods, period.id)
	}
	v.Reservations[requestID] = r
	l.reservation = requestID
	return nil
}

// Settle retains reservations for unknown amounts. Cancellation is not proof
// that the upstream did not bill the request.
func (l *Lease) Settle(ctx context.Context, actual Amount) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.reservation == "" {
		return nil
	}
	return l.store.change(ctx, func(v *state) error { return settle(v, l.reservation, 0, actual, false) })
}

func settle(v *state, id string, version int64, actual Amount, reconcile bool) error {
	r, ok := v.Reservations[id]
	if !ok {
		return errors.New("reservation not found")
	}
	if reconcile && (version != r.Version || r.State != "unknown") {
		return errors.New("reservation reconciliation conflict")
	}
	if reconcile && ((r.Actual.TokensKnown && actual.Tokens != r.Actual.Tokens) || (r.Actual.CreditsKnown && actual.Credits != r.Actual.Credits)) {
		return errors.New("verified usage cannot be changed")
	}
	if r.State == "settled" || r.State == "overrun" {
		return nil
	}
	if actual.Tokens < 0 || actual.Credits < 0 || actual.Tokens > 1_000_000_000_000 || actual.Credits > 1_000_000_000_000 {
		return errors.New("invalid actual usage")
	}
	if reconcile && ((r.Bound.TokensKnown && !actual.TokensKnown) || (r.Bound.CreditsKnown && !actual.CreditsKnown)) {
		return errors.New("reconciliation requires verified usage")
	}
	for _, id := range r.Periods {
		b, ok := v.Balances[id]
		if !ok {
			continue
		}
		if r.Bound.TokensKnown && actual.TokensKnown && !r.Actual.TokensKnown {
			b.ReservedTokens -= r.Bound.Tokens
			b.UsedTokens += actual.Tokens
		}
		if r.Bound.CreditsKnown && actual.CreditsKnown && !r.Actual.CreditsKnown {
			b.ReservedCredits -= r.Bound.Credits
			b.UsedCredits += actual.Credits
		}
		v.Balances[id] = b
	}
	if actual.TokensKnown && !r.Actual.TokensKnown {
		r.Actual.Tokens, r.Actual.TokensKnown = actual.Tokens, true
	}
	if actual.CreditsKnown && !r.Actual.CreditsKnown {
		r.Actual.Credits, r.Actual.CreditsKnown = actual.Credits, true
	}
	r.State = "unknown"
	if (!r.Bound.TokensKnown || r.Actual.TokensKnown) && (!r.Bound.CreditsKnown || r.Actual.CreditsKnown) {
		r.State = "settled"
		if (r.Bound.TokensKnown && r.Actual.Tokens > r.Bound.Tokens) || (r.Bound.CreditsKnown && r.Actual.Credits > r.Bound.Credits) {
			r.State = "overrun"
		}
	}
	r.Version++
	r.UpdatedAt = time.Now().UTC()
	v.Reservations[id] = r
	return nil
}

func (s *Store) Reconcile(ctx context.Context, id string, version int64, actual Amount) error {
	return s.change(ctx, func(v *state) error { return settle(v, id, version, actual, true) })
}

type usageKey struct{}

func WithMeter(ctx context.Context, report func(Amount)) context.Context {
	return context.WithValue(ctx, usageKey{}, report)
}

// ReportUsage only accepts integer fields from the verified response contract.
func ReportUsage(ctx context.Context, body []byte) {
	report, ok := ctx.Value(usageKey{}).(func(Amount))
	if !ok {
		return
	}
	type reportedUsage struct {
		TotalTokens *int64 `json:"total_tokens"`
		Credits     *int64 `json:"credits"`
	}
	var payload struct {
		Type     string         `json:"type"`
		Usage    *reportedUsage `json:"usage"`
		Response *struct {
			Usage *reportedUsage `json:"usage"`
		} `json:"response"`
	}
	if json.Unmarshal(body, &payload) != nil {
		return
	}
	reported := payload.Usage
	if payload.Response != nil && (payload.Type == "response.completed" || payload.Type == "response.incomplete" || payload.Type == "response.failed") {
		reported = payload.Response.Usage
	}
	if reported == nil {
		return
	}
	var actual Amount
	if n := reported.TotalTokens; n != nil && *n >= 0 {
		actual.Tokens, actual.TokensKnown = *n, true
	}
	if n := reported.Credits; n != nil && *n >= 0 {
		actual.Credits, actual.CreditsKnown = *n, true
	}
	report(actual)
}
