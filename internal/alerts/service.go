package alerts

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"reflect"
	"time"

	"clodesen2api/internal/controlstore"
)

const Document = "alert-notifications"

type Settings struct {
	Enabled    bool                       `json:"enabled"`
	Channels   map[string]ChannelSettings `json:"channels,omitempty"`
	URL        string                     `json:"url,omitempty"`
	LowBalance int64                      `json:"lowBalance"`
}

type ChannelSettings struct {
	Enabled bool              `json:"enabled"`
	Config  map[string]string `json:"config"`
}

type Event struct {
	ID        string    `json:"id"`
	Channel   string    `json:"channel,omitempty"`
	Condition string    `json:"condition"`
	Active    bool      `json:"active"`
	At        time.Time `json:"at"`
	Attempts  int       `json:"attempts"`
	RetryAt   time.Time `json:"retryAt"`
}

type View struct {
	Schema     int                      `json:"schema"`
	Revision   int64                    `json:"revision"`
	Settings   Settings                 `json:"settings"`
	Conditions map[string]bool          `json:"conditions"`
	Pending    []Event                  `json:"pending"`
	LastSent   *time.Time               `json:"lastSent,omitempty"`
	LastError  string                   `json:"lastError,omitempty"`
	Dropped    int                      `json:"dropped"`
	Claim      string                   `json:"claim,omitempty"`
	LeaseUntil time.Time                `json:"leaseUntil"`
	Delivery   map[string]DeliveryState `json:"delivery,omitempty"`
}

type DeliveryState struct {
	LastSent  *time.Time `json:"lastSent,omitempty"`
	LastError string     `json:"lastError,omitempty"`
}

func decode(raw json.RawMessage) (View, error) {
	v := View{Schema: 2, Revision: 1, Settings: Settings{LowBalance: 100}, Conditions: map[string]bool{}, Pending: []Event{}}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &v); err != nil {
			return v, err
		}
	}
	v.Settings = normalizeSettings(v.Settings)
	if v.Schema == 1 {
		v.Schema = 2
	}
	if v.Delivery == nil {
		v.Delivery = map[string]DeliveryState{}
	}
	if v.Schema != 2 || v.Revision < 1 || len(v.Pending) > 100 || len(v.Conditions) > 4 || v.Conditions == nil || len(v.Delivery) > len(definitions) {
		return v, errors.New("invalid notification state")
	}
	for id := range v.Delivery {
		if channelDefinition(id) == nil {
			return v, errors.New("invalid notification delivery")
		}
	}
	for name := range v.Conditions {
		if !validCondition(name) {
			return v, errors.New("invalid notification condition")
		}
	}
	for i := range v.Pending {
		event := &v.Pending[i]
		if event.Channel == "" {
			event.Channel = "webhook"
		}
		if event.ID == "" || !validCondition(event.Condition) || event.Attempts < 0 || event.Attempts > 10 || channelDefinition(event.Channel) == nil {
			return v, errors.New("invalid notification event")
		}
	}
	return v, v.Settings.Validate()
}

func validCondition(value string) bool {
	return value == "low_balance" || value == "no_accounts" || value == "job_failure" || value == "audit_degraded"
}

func ValidateDocument(raw json.RawMessage) error { _, err := decode(raw); return err }

func Load(ctx context.Context, store *controlstore.Store) (View, error) {
	var raw json.RawMessage
	if _, err := store.Load(ctx, Document, &raw); err != nil && !errors.Is(err, controlstore.ErrMissing) {
		return View{}, err
	}
	return decode(raw)
}

func Save(ctx context.Context, store *controlstore.Store, revision int64, settings Settings) (View, error) {
	return SaveWithClear(ctx, store, revision, settings, nil)
}

func SaveWithClear(ctx context.Context, store *controlstore.Store, revision int64, settings Settings, clearSecrets []string) (View, error) {
	settings = normalizeSettings(settings)
	var saved View
	err := store.Mutate(ctx, Document, func(raw json.RawMessage) (any, error) {
		v, err := decode(raw)
		if err != nil {
			return nil, err
		}
		if v.Revision != revision {
			return nil, controlstore.ErrConflict
		}
		merged, err := mergeSecrets(v.Settings, settings, clearSecrets)
		if err != nil {
			return nil, err
		}
		if err := merged.Validate(); err != nil {
			return nil, err
		}
		if !reflect.DeepEqual(v.Settings, merged) {
			// Invalidate an in-flight claim when its delivery settings change.
			v.Claim = ""
			v.LeaseUntil = time.Time{}
			thresholdChanged := v.Settings.LowBalance != merged.LowBalance
			pending := v.Pending[:0]
			for _, event := range v.Pending {
				if reflect.DeepEqual(v.Settings.Channels[event.Channel], merged.Channels[event.Channel]) && (!thresholdChanged || event.Condition != "low_balance") {
					pending = append(pending, event)
				}
			}
			v.Pending = pending
			for id, channel := range merged.Channels {
				if !reflect.DeepEqual(v.Settings.Channels[id], channel) {
					delete(v.Delivery, id)
					if channel.Enabled {
						for name, active := range v.Conditions {
							if active && (!thresholdChanged || name != "low_balance") && len(v.Pending) < 100 {
								v.Pending = append(v.Pending, Event{ID: randomID() + "-" + name, Channel: id, Condition: name, Active: true, At: time.Now()})
							}
						}
					}
				}
			}
			if thresholdChanged {
				delete(v.Conditions, "low_balance")
			}
		} else {
			for i := range v.Pending {
				if v.Pending[i].Attempts >= 10 {
					v.Pending[i].Attempts = 0
					v.Pending[i].RetryAt = time.Time{}
				}
			}
		}
		v.Revision++
		v.Settings = merged
		saved = v
		return v, nil
	})
	return saved, err
}

func Test(ctx context.Context, store *controlstore.Store, revision int64, channel string, draft ChannelSettings, clearSecrets []string) error {
	view, err := Load(ctx, store)
	if err != nil {
		return err
	}
	if view.Revision != revision {
		return controlstore.ErrConflict
	}
	settings, err := mergeSecrets(view.Settings, normalizeSettings(Settings{LowBalance: view.Settings.LowBalance, Channels: map[string]ChannelSettings{channel: draft}}), clearSecrets)
	if err != nil {
		return err
	}
	if err := validateChannel(channel, settings.Channels[channel], true); err != nil {
		return err
	}
	return deliver(ctx, channel, settings.Channels[channel], Event{ID: "test-" + randomID(), Channel: channel, Condition: "test", Active: true, At: time.Now()}, "Clodesen 告警测试", "这是一条通知渠道测试消息。")
}

// Observe persists transitions and claims at most one delivery across instances.
// Webhooks receive stable event IDs for receiver-side deduplication after crashes.
func Observe(ctx context.Context, store *controlstore.Store, observed map[string]bool, now time.Time) error {
	initial, err := Load(ctx, store)
	if err != nil || !initial.Settings.Enabled {
		return err
	}
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		return err
	}
	claim := hex.EncodeToString(random[:])
	var settings ChannelSettings
	var send *Event
	err = store.Mutate(ctx, Document, func(raw json.RawMessage) (any, error) {
		v, err := decode(raw)
		if err != nil {
			return nil, err
		}
		if !v.Settings.Enabled {
			return v, nil
		}
		for _, name := range []string{"low_balance", "no_accounts", "job_failure", "audit_degraded"} {
			active, known := observed[name]
			if !known || active == v.Conditions[name] {
				continue
			}
			v.Conditions[name] = active
			for _, definition := range ChannelDefinitions() {
				if !v.Settings.Channels[definition.ID].Enabled {
					continue
				}
				if len(v.Pending) == 100 {
					v.Dropped++
					continue
				}
				v.Pending = append(v.Pending, Event{ID: claim + "-" + definition.ID + "-" + name, Channel: definition.ID, Condition: name, Active: active, At: now})
			}
		}
		if v.LeaseUntil.After(now) {
			return v, nil
		}
		blocked := map[string]bool{}
		for i := range v.Pending {
			e := &v.Pending[i]
			if e.Attempts >= 10 || blocked[e.Channel] {
				continue
			}
			if e.RetryAt.After(now) {
				blocked[e.Channel] = true
				continue
			}
			e.Attempts++
			copy := *e
			send = &copy
			break
		}
		if send != nil {
			v.Claim = claim
			v.LeaseUntil = now.Add(time.Minute)
			settings = v.Settings.Channels[send.Channel]
		}
		return v, nil
	})
	if err != nil || send == nil {
		return err
	}
	work, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	title, content := eventCopy(*send)
	err = deliver(work, send.Channel, settings, *send, title, content)
	delivered := err == nil
	finish, done := context.WithTimeout(context.Background(), 3*time.Second)
	defer done()
	return store.Mutate(finish, Document, func(raw json.RawMessage) (any, error) {
		v, err := decode(raw)
		if err != nil {
			return nil, err
		}
		if v.Claim != claim {
			return v, nil
		}
		v.Claim = ""
		v.LeaseUntil = time.Time{}
		for i := range v.Pending {
			if v.Pending[i].ID != send.ID {
				continue
			}
			if delivered {
				v.Pending = append(v.Pending[:i], v.Pending[i+1:]...)
				v.LastSent = &now
				v.LastError = ""
				v.Delivery[send.Channel] = DeliveryState{LastSent: &now}
			} else {
				v.LastError = "delivery_failed"
				status := v.Delivery[send.Channel]
				status.LastError = "delivery_failed"
				v.Delivery[send.Channel] = status
				v.Pending[i].RetryAt = now.Add(time.Duration(1<<min(4, send.Attempts-1)) * time.Minute)
			}
			break
		}
		return v, nil
	})
}

func randomID() string {
	var data [16]byte
	if _, err := rand.Read(data[:]); err != nil {
		return hex.EncodeToString([]byte(time.Now().UTC().Format(time.RFC3339Nano)))
	}
	return hex.EncodeToString(data[:])
}
