// Package admission owns shared inference capacity and quota reservations.
package admission

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sync"
	"time"

	"clodesen2api/internal/controlstore"
)

const document = "inference-admission"

func ValidateDocument(raw json.RawMessage) error {
	var v state
	if err := json.Unmarshal(raw, &v); err != nil {
		return err
	}
	if err := v.Settings.Validate(); err != nil {
		return err
	}
	if v.Revision < 1 {
		return errors.New("invalid admission revision")
	}
	for id, b := range v.Balances {
		if id == "" || b.Until.IsZero() || b.UsedTokens < 0 || b.ReservedTokens < 0 || b.UsedCredits < 0 || b.ReservedCredits < 0 {
			return errors.New("invalid metering balance")
		}
	}
	for id, l := range v.Leases {
		if id == "" || l.Owner == "" || l.Bytes < 0 || l.Until.IsZero() {
			return errors.New("invalid admission lease")
		}
	}
	for id, c := range v.Counters {
		if id == "" || c.Requests < 0 || c.Until.IsZero() {
			return errors.New("invalid request counter")
		}
	}
	if len(v.Reservations) > 5000 {
		return errors.New("invalid reservation capacity")
	}
	for id, r := range v.Reservations {
		if id == "" || r.ID != id || r.Lease == "" || r.Version < 1 || r.Source != "upstream-usage-v1" || r.CreatedAt.IsZero() || r.UpdatedAt.IsZero() || len(r.Periods) != 2 {
			return errors.New("invalid metering reservation")
		}
		if r.State != "reserved" && r.State != "unknown" && r.State != "settled" && r.State != "overrun" {
			return errors.New("invalid reservation state")
		}
		for _, n := range []int64{r.Bound.Tokens, r.Bound.Credits, r.Actual.Tokens, r.Actual.Credits} {
			if n < 0 || n > 1_000_000_000_000 {
				return errors.New("invalid reservation amount")
			}
		}
		if !r.Bound.TokensKnown && !r.Bound.CreditsKnown {
			return errors.New("missing reservation units")
		}
		if (r.Bound.TokensKnown && r.Bound.Tokens == 0) || (r.Bound.CreditsKnown && r.Bound.Credits == 0) {
			return errors.New("missing reservation bound")
		}
		if r.State == "settled" || r.State == "overrun" {
			if (r.Bound.TokensKnown && !r.Actual.TokensKnown) || (r.Bound.CreditsKnown && !r.Actual.CreditsKnown) {
				return errors.New("incomplete settled usage")
			}
		}
		if r.Periods[0] == "" || r.Periods[1] == "" || r.Periods[0] == r.Periods[1] {
			return errors.New("invalid reservation periods")
		}
	}
	return nil
}

type Policy struct {
	MaxConcurrent   int   `json:"maxConcurrent"`
	DailyRequests   int64 `json:"dailyRequests"`
	MonthlyRequests int64 `json:"monthlyRequests"`
	DailyTokens     int64 `json:"dailyTokens"`
	MonthlyTokens   int64 `json:"monthlyTokens"`
	DailyCredits    int64 `json:"dailyCredits"`
	MonthlyCredits  int64 `json:"monthlyCredits"`
}

func (p Policy) Validate() error {
	if p.MaxConcurrent < 0 || p.MaxConcurrent > 10000 {
		return errors.New("maxConcurrent must be between 0 and 10000")
	}
	for _, value := range []int64{p.DailyRequests, p.MonthlyRequests, p.DailyTokens, p.MonthlyTokens, p.DailyCredits, p.MonthlyCredits} {
		if value < 0 || value > 1_000_000_000_000 {
			return errors.New("quota must be between 0 and 1000000000000")
		}
	}
	return nil
}

type Settings struct {
	MaxConcurrent           int    `json:"maxConcurrent"`
	ModelConcurrent         int    `json:"modelConcurrent"`
	BodyBytes               int64  `json:"bodyBytes"`
	BodyTimeoutSeconds      int    `json:"bodyTimeoutSeconds"`
	RequestTimeoutSeconds   int    `json:"requestTimeoutSeconds"`
	IdleTimeoutSeconds      int    `json:"idleTimeoutSeconds"`
	FirstByteTimeoutSeconds int    `json:"firstByteTimeoutSeconds"`
	MaxAttempts             int    `json:"maxAttempts"`
	Timezone                string `json:"timezone"`
}

func Defaults() Settings {
	return Settings{MaxConcurrent: 64, ModelConcurrent: 16, BodyBytes: 768 << 20, BodyTimeoutSeconds: 30, RequestTimeoutSeconds: 600, IdleTimeoutSeconds: 90, FirstByteTimeoutSeconds: 90, MaxAttempts: 3, Timezone: "Asia/Shanghai"}
}
func (s Settings) Validate() error {
	if s.MaxConcurrent < 1 || s.MaxConcurrent > 10000 || s.ModelConcurrent < 1 || s.ModelConcurrent > 10000 || s.BodyBytes < 1<<20 || s.BodyBytes > 8<<30 || s.BodyTimeoutSeconds < 1 || s.BodyTimeoutSeconds > 600 || s.RequestTimeoutSeconds < 1 || s.RequestTimeoutSeconds > 86400 || s.IdleTimeoutSeconds < 1 || s.IdleTimeoutSeconds > 3600 || s.MaxAttempts < 1 || s.MaxAttempts > 32 {
		return errors.New("invalid admission limits")
	}
	if s.FirstByteTimeoutSeconds < 1 || s.FirstByteTimeoutSeconds > 3600 {
		return errors.New("invalid first byte timeout")
	}
	_, err := time.LoadLocation(s.Timezone)
	return err
}

type lease struct {
	Key, Model string
	Owner      string
	Bytes      int64
	Until      time.Time
}
type counter struct {
	Requests int64     `json:"requests"`
	Until    time.Time `json:"until"`
}
type period struct {
	id      string
	limit   int64
	until   time.Time
	monthly bool
}
type state struct {
	Settings     Settings               `json:"settings"`
	Revision     int64                  `json:"revision"`
	Leases       map[string]lease       `json:"leases"`
	Counters     map[string]counter     `json:"counters"`
	Rates        map[string][]time.Time `json:"rates"`
	Balances     map[string]Balance     `json:"balances,omitempty"`
	Reservations map[string]Reservation `json:"reservations,omitempty"`
}
type View struct {
	Settings     Settings               `json:"settings"`
	Revision     int64                  `json:"revision"`
	Active       int                    `json:"active"`
	BodyBytes    int64                  `json:"bodyBytes"`
	Counters     map[string]counter     `json:"counters,omitempty"`
	Balances     map[string]Balance     `json:"balances,omitempty"`
	Reservations map[string]Reservation `json:"reservations,omitempty"`
}
type Error struct {
	Status     int
	Code       string
	RetryAfter int
}

func (e *Error) Error() string { return e.Code }

type Store struct {
	control *controlstore.Store
	owner   string
}

func Open(control *controlstore.Store) (*Store, error) {
	var owner [16]byte
	if _, err := rand.Read(owner[:]); err != nil {
		return nil, err
	}
	s := &Store{control: control, owner: hex.EncodeToString(owner[:])}
	err := s.change(context.Background(), func(*state) error { return nil })
	return s, err
}
func decode(raw json.RawMessage) (state, error) {
	v := state{Settings: Defaults(), Revision: 1, Leases: map[string]lease{}, Counters: map[string]counter{}}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &v); err != nil {
			return v, err
		}
	}
	if v.Leases == nil {
		v.Leases = map[string]lease{}
	}
	if v.Counters == nil {
		v.Counters = map[string]counter{}
	}
	if v.Rates == nil {
		v.Rates = map[string][]time.Time{}
	}
	if v.Balances == nil {
		v.Balances = map[string]Balance{}
	}
	if v.Reservations == nil {
		v.Reservations = map[string]Reservation{}
	}
	if err := v.Settings.Validate(); err != nil {
		return v, err
	}
	now := time.Now()
	for id, l := range v.Leases {
		if !l.Until.After(now) {
			delete(v.Leases, id)
		}
	}
	for id, c := range v.Counters {
		if !c.Until.After(now) {
			delete(v.Counters, id)
		}
	}
	for id, b := range v.Balances {
		if !b.Until.After(now) {
			delete(v.Balances, id)
		}
	}
	for id, r := range v.Reservations {
		if r.State == "reserved" {
			if _, live := v.Leases[r.Lease]; !live {
				r.State = "unknown"
				r.Version++
				r.UpdatedAt = now
				v.Reservations[id] = r
			}
		}
		if now.Sub(r.CreatedAt) > 63*24*time.Hour {
			delete(v.Reservations, id)
		}
	}
	for id, stamps := range v.Rates {
		kept := stamps[:0]
		for _, stamp := range stamps {
			if stamp.After(now.Add(-time.Minute)) {
				kept = append(kept, stamp)
			}
		}
		if len(kept) == 0 {
			delete(v.Rates, id)
		} else {
			v.Rates[id] = kept
		}
	}
	return v, nil
}
func (s *Store) change(ctx context.Context, f func(*state) error) error {
	return s.control.Mutate(ctx, document, func(raw json.RawMessage) (any, error) {
		v, err := decode(raw)
		if err != nil {
			return nil, err
		}
		if err = f(&v); err != nil {
			return nil, err
		}
		return v, nil
	})
}
func (s *Store) Snapshot(ctx context.Context) (View, error) {
	var raw json.RawMessage
	_, err := s.control.Load(ctx, document, &raw)
	if err != nil {
		return View{}, err
	}
	v, err := decode(raw)
	if err != nil {
		return View{}, err
	}
	result := View{Settings: v.Settings, Revision: v.Revision, Active: len(v.Leases), Counters: v.Counters, Balances: v.Balances, Reservations: v.Reservations}
	for _, l := range v.Leases {
		result.BodyBytes += l.Bytes
	}
	return result, nil
}
func (s *Store) Update(ctx context.Context, revision int64, settings Settings) (View, error) {
	if err := settings.Validate(); err != nil {
		return View{}, err
	}
	err := s.change(ctx, func(v *state) error {
		if revision != v.Revision {
			return controlstore.ErrConflict
		}
		if settings.Timezone != v.Settings.Timezone && len(v.Counters) > 0 {
			return errors.New("cannot change quota timezone before existing periods expire")
		}
		v.Settings = settings
		v.Revision++
		return nil
	})
	if err != nil {
		return View{}, err
	}
	return s.Snapshot(ctx)
}

type Lease struct {
	store       *Store
	id          string
	Settings    Settings
	mu          sync.Mutex
	released    bool
	hardUntil   time.Time
	reservation string
}

func (s *Store) Acquire(ctx context.Context, key string, p Policy, bodyBytes int64) (*Lease, error) {
	if err := p.Validate(); err != nil {
		return nil, err
	}
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		return nil, err
	}
	l := &Lease{store: s, id: hex.EncodeToString(random[:])}
	err := s.change(ctx, func(v *state) error {
		var count int
		var bytes int64
		for _, current := range v.Leases {
			bytes += current.Bytes
			if current.Key == key {
				count++
			}
		}
		if len(v.Leases) >= v.Settings.MaxConcurrent || bodyBytes < 0 || bytes+bodyBytes > v.Settings.BodyBytes {
			return &Error{503, "global_capacity_exceeded", 1}
		}
		if p.MaxConcurrent > 0 && count >= p.MaxConcurrent {
			return &Error{429, "key_concurrency_exceeded", 1}
		}
		l.Settings = v.Settings
		l.hardUntil = time.Now().Add(time.Duration(v.Settings.RequestTimeoutSeconds+v.Settings.BodyTimeoutSeconds) * time.Second)
		v.Leases[l.id] = lease{Key: key, Owner: s.owner, Bytes: bodyBytes, Until: l.hardUntil.Add(30 * time.Second)}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return l, nil
}

// Admit counts one client request, independent of the number of upstream attempts.
// Token/credit hard quotas require an explicitly verified upstream bound.
func (l *Lease) Admit(ctx context.Context, model string, p Policy, rpm *int) error {
	return l.AdmitChecked(ctx, model, p, rpm, nil)
}

type Check func(controlstore.Reader) (Policy, *int, error)

// AdmitChecked checks current key restrictions and consumes quota in one transaction.
func (l *Lease) AdmitChecked(ctx context.Context, model string, p Policy, rpm *int, check Check) error {
	return l.AdmitMetered(ctx, model, p, rpm, check, Meter{}, "")
}

func (l *Lease) AdmitMetered(ctx context.Context, model string, p Policy, rpm *int, check Check, meter Meter, requestID string) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.released {
		return errors.New("admission lease released")
	}
	previous := l.reservation
	err := l.store.control.MutateWithRead(ctx, document, func(raw json.RawMessage, read controlstore.Reader) (any, error) {
		v, err := decode(raw)
		if err != nil {
			return nil, err
		}
		err = l.admit(&v, model, p, rpm, check, read, meter, requestID)
		return v, err
	})
	if err != nil {
		l.reservation = previous
	}
	return err
}

func (l *Lease) admit(v *state, model string, p Policy, rpm *int, check Check, read controlstore.Reader, meter Meter, requestID string) error {
	if check != nil {
		var err error
		p, rpm, err = check(read)
		if err != nil {
			return err
		}
	}
	current, ok := v.Leases[l.id]
	if !ok || current.Owner != l.store.owner {
		return &Error{503, "admission_lease_expired", 1}
	}
	if current.Model != "" {
		return nil
	}
	if p.MaxConcurrent > 0 {
		count := 0
		for _, other := range v.Leases {
			if other.Key == current.Key {
				count++
			}
		}
		if count > p.MaxConcurrent {
			return &Error{429, "key_concurrency_exceeded", 1}
		}
	}
	if rpm != nil && len(v.Rates[current.Key]) >= *rpm {
		return &Error{429, "rpm_exceeded", int(time.Until(v.Rates[current.Key][0].Add(time.Minute)).Seconds()) + 1}
	}
	count := 0
	for _, other := range v.Leases {
		if other.Model == model {
			count++
		}
	}
	if count >= v.Settings.ModelConcurrent {
		return &Error{503, "model_capacity_exceeded", 1}
	}
	loc, err := time.LoadLocation(v.Settings.Timezone)
	if err != nil {
		return err
	}
	now := time.Now().In(loc)
	day := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, loc)
	month := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, loc)
	periods := []period{{current.Key + "/day/" + day.Format("2006-01-02"), p.DailyRequests, day.AddDate(0, 0, 1), false}, {current.Key + "/month/" + month.Format("2006-01"), p.MonthlyRequests, month.AddDate(0, 1, 0), true}}
	for _, period := range periods {
		if period.limit > 0 && v.Counters[period.id].Requests >= period.limit {
			return &Error{429, "request_quota_exceeded", int(time.Until(period.until).Seconds()) + 1}
		}
	}
	if err := l.reserve(v, current, p, periods, meter, requestID); err != nil {
		return err
	}
	for _, period := range periods {
		c := v.Counters[period.id]
		c.Requests++
		c.Until = period.until
		v.Counters[period.id] = c
	}
	if rpm != nil {
		v.Rates[current.Key] = append(v.Rates[current.Key], time.Now())
	}
	current.Model = model
	v.Leases[l.id] = current
	return nil
}

// Bind renews the lease while its request is alive. Losing storage or ownership
// cancels upstream I/O before another instance may reuse the capacity.
func (l *Lease) Bind(parent context.Context) (context.Context, context.CancelFunc, error) {
	ctx, cancel := context.WithCancel(parent)
	if err := l.renew(ctx); err != nil {
		cancel()
		return nil, nil, err
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				renewCtx, stop := context.WithTimeout(ctx, 4*time.Second)
				err := l.renew(renewCtx)
				stop()
				if err != nil {
					cancel()
					return
				}
			}
		}
	}()
	return ctx, func() { cancel(); <-done }, nil
}

func (l *Lease) renew(ctx context.Context) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.released {
		return errors.New("admission lease released")
	}
	return l.store.change(ctx, func(v *state) error {
		current, ok := v.Leases[l.id]
		if !ok || current.Owner != l.store.owner || !l.hardUntil.After(time.Now()) {
			return &Error{503, "admission_lease_expired", 1}
		}
		current.Until = time.Now().Add(15 * time.Second)
		if current.Until.After(l.hardUntil.Add(5 * time.Second)) {
			current.Until = l.hardUntil.Add(5 * time.Second)
		}
		v.Leases[l.id] = current
		return nil
	})
}
func (l *Lease) Release() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.released {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	err := l.store.change(ctx, func(v *state) error { delete(v.Leases, l.id); return nil })
	if err == nil {
		l.released = true
	}
	return err
}

type limitsContextKey struct{}

func WithLimits(ctx context.Context, settings Settings) context.Context {
	return context.WithValue(ctx, limitsContextKey{}, settings)
}
func Limits(ctx context.Context) Settings {
	if v, ok := ctx.Value(limitsContextKey{}).(Settings); ok {
		return v
	}
	return Defaults()
}
