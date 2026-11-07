package config

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"time"

	"clodesen2api/internal/controlstore"
	"github.com/lfhy/flag"
)

type FieldState struct {
	Source    string          `json:"source"`
	Locked    bool            `json:"locked"`
	Mode      string          `json:"mode"`
	Pending   bool            `json:"pending"`
	Effective json.RawMessage `json:"effective,omitempty"`
	Stored    json.RawMessage `json:"stored,omitempty"`
}

var settingNames = map[string]string{
	"addr": "server.addr", "base-url": "upstream.baseURL", "openai-base-url": "upstream.openAIBaseURL",
	"login-path": "upstream.loginPath", "login-timeout": "upstream.loginTimeout", "request-timeout": "upstream.requestTimeout", "user-agent": "upstream.userAgent",
	"strategy": "auth.strategy", "account-rpm": "auth.rpm", "auto-relogin": "auth.autoRelogin",
	"api-keys-file": "api.keysFile", "api-allow-no-auth": "api.allowNoAuth", "api-trusted-proxies": "api.trustedProxies",
	"checkin": "checkin.enable", "checkin-path": "checkin.path", "checkin-at": "checkin.at", "checkin-tz": "checkin.timezone",
	"auto-redeem": "redemption.enable", "auto-redeem-at": "redemption.at", "auto-redeem-code": "redemption.code",
	"update-source": "update.source", "update-custom-prefix": "update.customPrefix",
	"cache-accounts-seconds": "cache.accountsSeconds", "cache-models-seconds": "cache.modelsSeconds", "cache-credits-seconds": "cache.creditsSeconds",
}

func (c *Config) captureSources(fs *flag.FlagSet, args []string, lookup func(string) (string, bool)) error {
	c.sources = map[string]string{}
	cli := map[string]bool{}
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			break
		}
		if !strings.HasPrefix(arg, "-") {
			continue
		}
		name := strings.SplitN(strings.TrimLeft(arg, "-"), "=", 2)[0]
		if f := fs.Lookup(name); f != nil {
			cli[f.Name] = true
			boolean, ok := f.Value.(interface{ IsBoolFlag() bool })
			if !strings.Contains(arg, "=") && (!ok || !boolean.IsBoolFlag()) {
				i++
			}
		}
	}
	fs.Visit(func(f *flag.Flag) {
		if path := settingNames[f.Name]; path != "" {
			c.sources[path] = "file"
		}
	})
	var result error
	fs.VisitAll(func(f *flag.Flag) {
		if f.ConfigTitle == "" || result != nil {
			return
		}
		path := settingNames[f.Name]
		if cli[f.Name] {
			if path != "" {
				c.sources[path] = "cli"
			}
			return
		}
		name := "CLODESEN2API_" + strings.ToUpper(f.ConfigTitle+"_"+f.ConfigKey)
		if value, ok := lookup(name); ok {
			if err := f.Value.Set(value); err != nil {
				result = fmt.Errorf("%s: invalid value", name)
				return
			}
			if path != "" {
				c.sources[path] = "environment"
			}
		}
	})
	return result
}

func hotSetting(path string) bool {
	switch path {
	case "auth.strategy", "auth.rpm", "checkin.enable", "checkin.at", "redemption.enable", "redemption.at", "redemption.code", "update.source", "update.customPrefix", "cache.accountsSeconds", "cache.modelsSeconds", "cache.creditsSeconds":
		return true
	}
	return false
}

type settingsDocument struct {
	Schema                 int                        `json:"schema"`
	Values                 map[string]json.RawMessage `json:"values"`
	LegacyStrategyImported bool                       `json:"legacyStrategyImported,omitempty"`
	ScheduleRearms         map[string]time.Time       `json:"scheduleRearms,omitempty"`
}

func (s *SettingsStore) ImportLegacyStrategy(ctx context.Context, strategy string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, doc, revision, err := s.snapshot(ctx)
	if err != nil {
		return err
	}
	if doc.LegacyStrategyImported {
		return nil
	}
	doc.LegacyStrategyImported = true
	if _, exists := doc.Values["auth.strategy"]; !exists && s.base.sources["auth.strategy"] != "cli" && s.base.sources["auth.strategy"] != "environment" {
		strategy = StoredStrategy(strategy)
		if strategy == "" {
			return errors.New("invalid legacy strategy")
		}
		doc.Values["auth.strategy"], _ = json.Marshal(strategy)
	}
	if _, err := s.db.Save(ctx, "settings", revision, doc); err != nil {
		return err
	}
	_, _, err = s.viewAfter(ctx)
	return err
}

type SettingsStore struct {
	mu      sync.Mutex
	db      *controlstore.Store
	base    Config
	running Config
}

// SettingsStore keeps startup configuration immutable. Only fields explicitly
// classified as hot are published to the service while it is running.
func OpenSettings(ctx context.Context, db *controlstore.Store, cfg *Config) (*SettingsStore, error) {
	s := &SettingsStore{db: db, base: *cfg, running: *cfg}
	if strategy := StoredStrategy(s.base.Auth.Strategy); strategy != "" {
		s.base.Auth.Strategy = strategy
	}
	doc := settingsDocument{Schema: 1, Values: map[string]json.RawMessage{}}
	if _, err := db.Load(ctx, "settings", &doc); errors.Is(err, controlstore.ErrMissing) {
		if err := db.Import(ctx, "settings", cfg.ConfigFilePath(), doc); err != nil {
			return nil, err
		}
	} else if err != nil {
		return nil, err
	}
	effective, _, _, err := s.snapshot(ctx)
	if err != nil {
		return nil, err
	}
	s.running = effective
	return s, nil
}

func flattenSettings(value PublicSettings) map[string]json.RawMessage {
	var groups map[string]map[string]json.RawMessage
	// Metadata is never stored as a configurable value.
	value.Revision, value.Fields = 0, nil
	raw, _ := json.Marshal(value)
	_ = json.Unmarshal(raw, &groups)
	out := map[string]json.RawMessage{}
	for group, fields := range groups {
		for field, value := range fields {
			if field != "codeConfigured" {
				out[group+"."+field] = value
			}
		}
	}
	return out
}

func applyValues(base Config, values map[string]json.RawMessage) (Config, error) {
	fields := flattenSettings(base.PublicSettings())
	for name, raw := range values {
		if _, ok := fields[name]; !ok {
			return Config{}, errors.New("unknown stored setting")
		}
		fields[name] = raw
	}
	groups := map[string]map[string]json.RawMessage{}
	for name, raw := range fields {
		parts := strings.SplitN(name, ".", 2)
		if groups[parts[0]] == nil {
			groups[parts[0]] = map[string]json.RawMessage{}
		}
		groups[parts[0]][parts[1]] = raw
	}
	raw, err := json.Marshal(groups)
	if err != nil {
		return Config{}, err
	}
	var public PublicSettings
	if err := json.Unmarshal(raw, &public); err != nil {
		return Config{}, err
	}
	public.Auth.Strategy = StoredStrategy(public.Auth.Strategy)
	if err := base.ApplyPublicSettings(public); err != nil {
		return Config{}, err
	}
	return base, nil
}

func (s *SettingsStore) snapshot(ctx context.Context) (Config, settingsDocument, int64, error) {
	var doc settingsDocument
	revision, err := s.db.Load(ctx, "settings", &doc)
	if err != nil {
		return Config{}, doc, 0, err
	}
	if doc.Schema != 1 || doc.Values == nil {
		return Config{}, doc, 0, errors.New("invalid settings schema")
	}
	if raw := doc.Values["auth.strategy"]; len(raw) > 0 {
		var previous string
		if json.Unmarshal(raw, &previous) == nil {
			if strategy := StoredStrategy(previous); strategy != "" && strategy != previous {
				doc.Values["auth.strategy"], _ = json.Marshal(strategy)
			}
		}
	}
	values := map[string]json.RawMessage{}
	for path, value := range doc.Values {
		if source := s.base.sources[path]; source != "cli" && source != "environment" {
			values[path] = value
		}
	}
	effective, err := applyValues(s.base, values)
	effective.ScheduleRearms = doc.ScheduleRearms
	return effective, doc, revision, err
}

func (s *SettingsStore) Startup() Config { s.mu.Lock(); defer s.mu.Unlock(); return s.running }

func (s *SettingsStore) View(ctx context.Context) (PublicSettings, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.view(ctx)
}

func (s *SettingsStore) view(ctx context.Context) (PublicSettings, error) {
	effective, doc, revision, err := s.snapshot(ctx)
	if err != nil {
		return PublicSettings{}, err
	}
	out := effective.PublicSettings()
	out.Revision, out.Fields = revision, map[string]FieldState{}
	running := flattenSettings(s.running.PublicSettings())
	for path, value := range flattenSettings(out) {
		source := s.base.sources[path]
		if source == "" {
			source = "default"
		}
		locked := source == "cli" || source == "environment"
		if _, exists := doc.Values[path]; exists && !locked {
			source = "management"
		}
		mode := "restart"
		if hotSetting(path) {
			mode = "hot"
		}
		field := FieldState{Source: source, Locked: locked, Mode: mode, Pending: mode == "restart" && !reflect.DeepEqual(value, running[path])}
		if path != "redemption.code" {
			field.Effective = running[path]
			field.Stored = doc.Values[path]
		}
		out.Fields[path] = field
	}
	out.Redemption.Code = ""
	return out, nil
}

func (s *SettingsStore) Save(ctx context.Context, settings PublicSettings) (PublicSettings, Config, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	current, doc, revision, err := s.snapshot(ctx)
	if err != nil {
		return PublicSettings{}, Config{}, err
	}
	if settings.Revision != revision {
		return PublicSettings{}, Config{}, controlstore.ErrConflict
	}
	candidate := current
	if err := candidate.ApplyPublicSettings(settings); err != nil {
		return PublicSettings{}, Config{}, err
	}
	before, after := flattenSettings(current.PublicSettings()), flattenSettings(candidate.PublicSettings())
	for path, value := range after {
		if reflect.DeepEqual(value, before[path]) {
			continue
		}
		if source := s.base.sources[path]; source == "cli" || source == "environment" {
			return PublicSettings{}, Config{}, fmt.Errorf("%s is locked by %s", path, source)
		}
		doc.Values[path] = value
	}
	if doc.ScheduleRearms == nil {
		doc.ScheduleRearms = map[string]time.Time{}
	}
	now := time.Now()
	rearm := func(kind, oldAt, at, tz string) {
		if oldAt == at {
			return
		}
		loc, err := time.LoadLocation(tz)
		if err != nil {
			return
		}
		hm, err := time.Parse("15:04", at)
		if err != nil {
			return
		}
		local := now.In(loc)
		target := time.Date(local.Year(), local.Month(), local.Day(), hm.Hour(), hm.Minute(), 0, 0, loc)
		delete(doc.ScheduleRearms, kind)
		if target.After(now) {
			doc.ScheduleRearms[kind] = target.UTC()
		}
	}
	rearm("checkin", current.Checkin.At, candidate.Checkin.At, candidate.Checkin.Timezone)
	rearm("redeem", current.Redemption.At, candidate.Redemption.At, "Asia/Shanghai")
	if _, err := s.db.Save(ctx, "settings", revision, doc); err != nil {
		return PublicSettings{}, Config{}, err
	}
	return s.viewAfter(ctx)
}

func (s *SettingsStore) ViewAfter(ctx context.Context) (PublicSettings, Config, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.viewAfter(ctx)
}

func (s *SettingsStore) viewAfter(ctx context.Context) (PublicSettings, Config, error) {
	effective, _, _, err := s.snapshot(ctx)
	if err != nil {
		return PublicSettings{}, Config{}, err
	}
	values := map[string]json.RawMessage{}
	for path, value := range flattenSettings(effective.PublicSettings()) {
		if hotSetting(path) {
			values[path] = value
		}
	}
	hot, err := applyValues(s.running, values)
	if err != nil {
		return PublicSettings{}, Config{}, err
	}
	hot.ScheduleRearms = effective.ScheduleRearms
	s.running = hot
	view, err := s.view(ctx)
	return view, hot, err
}
