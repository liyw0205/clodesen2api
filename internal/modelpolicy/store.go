// Package modelpolicy owns the public model namespace and routing decisions.
package modelpolicy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"regexp"
	"sort"
	"strings"

	"clodesen2api/internal/admission"
	"clodesen2api/internal/controlstore"
	"clodesen2api/internal/openai"
)

const document = "model-policies"

var ErrInvalid = errors.New("invalid model policy")

func ValidateDocument(raw json.RawMessage) error {
	var value saved
	if err := json.Unmarshal(raw, &value); err != nil {
		return err
	}
	return validate(value)
}

type Rule struct {
	SourceID     string          `json:"sourceId"`
	PublicID     string          `json:"publicId"`
	UpstreamID   string          `json:"upstreamId"`
	Enabled      bool            `json:"enabled"`
	KeepOriginal bool            `json:"keepOriginal"`
	Endpoints    []string        `json:"endpoints"`
	Stream       *bool           `json:"stream"`
	Metering     admission.Meter `json:"metering"`
}

type saved struct {
	Catalog []string        `json:"catalog"`
	Rules   map[string]Rule `json:"rules"`
}

type Snapshot struct {
	Revision int64  `json:"revision"`
	Models   []Rule `json:"models"`
}

type Store struct{ control *controlstore.Store }

func Open(control *controlstore.Store) (*Store, error) {
	s := &Store{control: control}
	if _, err := s.Snapshot(context.Background()); errors.Is(err, controlstore.ErrMissing) {
		_, err = control.Save(context.Background(), document, 0, saved{Rules: map[string]Rule{}})
		if err != nil && !errors.Is(err, controlstore.ErrConflict) {
			return nil, err
		}
	} else if err != nil {
		return nil, err
	}
	return s, nil
}

func (s *Store) load(ctx context.Context) (saved, int64, error) {
	var value saved
	revision, err := s.control.Load(ctx, document, &value)
	if err != nil {
		return value, 0, err
	}
	if value.Rules == nil {
		value.Rules = make(map[string]Rule)
	}
	if err := validate(value); err != nil {
		return value, 0, err
	}
	return value, revision, nil
}

func (s *Store) Snapshot(ctx context.Context) (Snapshot, error) {
	value, revision, err := s.load(ctx)
	if err != nil {
		return Snapshot{}, err
	}
	result := Snapshot{Revision: revision, Models: make([]Rule, 0, len(value.Catalog))}
	seen := make(map[string]bool)
	for _, source := range value.Catalog {
		rule, ok := value.Rules[source]
		if !ok {
			rule = Rule{SourceID: source, PublicID: source, UpstreamID: source, Enabled: true}
		}
		result.Models = append(result.Models, rule)
		seen[source] = true
	}
	for source, rule := range value.Rules {
		if !seen[source] {
			result.Models = append(result.Models, rule)
		}
	}
	sort.Slice(result.Models, func(i, j int) bool { return result.Models[i].SourceID < result.Models[j].SourceID })
	return result, nil
}

// Register merges discovered IDs so a partial upstream response cannot erase
// configured identities. Policies remain valid during a catalog outage.
func (s *Store) Register(ctx context.Context, ids []string) error {
	for attempt := 0; attempt < 4; attempt++ {
		value, revision, err := s.load(ctx)
		if err != nil {
			return err
		}
		set := make(map[string]bool, len(value.Catalog))
		for _, id := range value.Catalog {
			set[id] = true
		}
		for _, id := range ids {
			if !validID(id) {
				return errors.New("invalid catalog model ID")
			}
			set[id] = true
		}
		catalog := make([]string, 0, len(set))
		for id := range set {
			catalog = append(catalog, id)
		}
		sort.Strings(catalog)
		if reflect.DeepEqual(catalog, value.Catalog) {
			return nil
		}
		value.Catalog = catalog
		if err := validate(value); err != nil {
			return err
		}
		_, err = s.control.Save(ctx, document, revision, value)
		if errors.Is(err, controlstore.ErrConflict) {
			continue
		}
		return err
	}
	return controlstore.ErrConflict
}

func (s *Store) Put(ctx context.Context, revision int64, rule Rule) (Snapshot, error) {
	value, current, err := s.load(ctx)
	if err != nil {
		return Snapshot{}, err
	}
	if current != revision {
		return Snapshot{}, controlstore.ErrConflict
	}
	known := false
	for _, id := range value.Catalog {
		if id == rule.SourceID {
			known = true
		}
	}
	if !known {
		return Snapshot{}, fmt.Errorf("%w: source model is not in the discovered catalog", ErrInvalid)
	}
	value.Rules[rule.SourceID] = rule
	if err := validate(value); err != nil {
		return Snapshot{}, fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	if _, err := s.control.Save(ctx, document, revision, value); err != nil {
		return Snapshot{}, err
	}
	return s.Snapshot(ctx)
}

func (s *Store) Delete(ctx context.Context, revision int64, source string) (Snapshot, error) {
	value, current, err := s.load(ctx)
	if err != nil {
		return Snapshot{}, err
	}
	if current != revision {
		return Snapshot{}, controlstore.ErrConflict
	}
	if _, ok := value.Rules[source]; !ok {
		return Snapshot{}, controlstore.ErrMissing
	}
	delete(value.Rules, source)
	if err := validate(value); err != nil {
		return Snapshot{}, err
	}
	if _, err := s.control.Save(ctx, document, revision, value); err != nil {
		return Snapshot{}, err
	}
	return s.Snapshot(ctx)
}

// Resolve uses a single snapshot for name resolution and endpoint policy.
func (s Snapshot) Resolve(name, path string, stream bool) (Rule, error) {
	if !validID(name) {
		return Rule{}, errors.New("a valid model ID is required")
	}
	for _, rule := range s.Models {
		if name != rule.PublicID && !(name == rule.SourceID && (rule.KeepOriginal || rule.PublicID == rule.SourceID)) {
			continue
		}
		chain, err := s.chain(rule)
		if err != nil {
			return Rule{}, err
		}
		for _, target := range chain {
			if len(target.Endpoints) > 0 {
				found := false
				for _, endpoint := range target.Endpoints {
					if path == endpoint {
						found = true
					}
				}
				if !found {
					return Rule{}, errors.New("model does not support this endpoint")
				}
			}
			if stream && target.Stream != nil && !*target.Stream {
				return Rule{}, errors.New("model does not support streaming")
			}
		}
		rule.UpstreamID = chain[len(chain)-1].UpstreamID
		rule.Metering = chain[len(chain)-1].Metering
		return rule, nil
	}
	// Preserve passthrough models until discovered, while reserving every
	// configured source, alias and upstream name against bypasses.
	for _, rule := range s.Models {
		if strings.EqualFold(name, rule.SourceID) || strings.EqualFold(name, rule.UpstreamID) || strings.EqualFold(name, rule.PublicID) {
			return Rule{}, errors.New("use the configured public model ID")
		}
	}
	return Rule{SourceID: name, PublicID: name, UpstreamID: name, Enabled: true}, nil
}

func (s Snapshot) Available(rule Rule) bool {
	_, err := s.chain(rule)
	return err == nil
}

func (s Snapshot) Route(rule Rule) (Rule, error) {
	chain, err := s.chain(rule)
	if err != nil {
		return Rule{}, err
	}
	rule.UpstreamID = chain[len(chain)-1].UpstreamID
	return rule, nil
}

func (s Snapshot) chain(rule Rule) ([]Rule, error) {
	bySource := make(map[string]Rule, len(s.Models))
	for _, target := range s.Models {
		bySource[target.SourceID] = target
	}
	seen := map[string]bool{}
	var chain []Rule
	for {
		if seen[rule.SourceID] {
			return nil, errors.New("cyclic model mapping")
		}
		if !rule.Enabled {
			return nil, errors.New("model is disabled")
		}
		seen[rule.SourceID] = true
		chain = append(chain, rule)
		if rule.UpstreamID == rule.SourceID {
			return chain, nil
		}
		next, ok := bySource[rule.UpstreamID]
		if !ok {
			return nil, errors.New("model target is unavailable")
		}
		rule = next
	}
}

var modelID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.:/@+\-]{0,255}$`)

func validID(value string) bool { return modelID.MatchString(value) }

func validate(value saved) error {
	if len(value.Catalog) > 4096 || len(value.Rules) > 4096 {
		return errors.New("model catalog capacity exceeded")
	}
	known := make(map[string]bool, len(value.Catalog))
	namespace := make(map[string]string)
	claim := func(name, source string) error {
		key := strings.ToLower(name)
		if old, ok := namespace[key]; ok && old != source {
			return fmt.Errorf("model ID collision: %s", name)
		}
		namespace[key] = source
		return nil
	}
	for _, id := range value.Catalog {
		if !validID(id) || known[id] {
			return errors.New("invalid model catalog")
		}
		known[id] = true
		if err := claim(id, id); err != nil {
			return err
		}
	}
	for source, rule := range value.Rules {
		if err := rule.Metering.Validate(); err != nil {
			return err
		}
		if source != rule.SourceID || !known[source] || !validID(rule.PublicID) || !known[rule.UpstreamID] {
			return errors.New("invalid model mapping or dangling upstream")
		}
		if err := claim(rule.PublicID, source); err != nil {
			return err
		}
		if len(rule.Endpoints) > 4 {
			return errors.New("too many endpoint capabilities")
		}
		seen := map[string]bool{}
		for _, endpoint := range rule.Endpoints {
			if seen[endpoint] {
				return errors.New("duplicate endpoint capability")
			}
			seen[endpoint] = true
			switch endpoint {
			case openai.ChatCompletionsPath, openai.ResponsesPath, openai.GenerationsPath, openai.EditsPath:
			default:
				return errors.New("unsupported endpoint capability")
			}
		}
	}
	for source := range value.Rules {
		seen := map[string]bool{}
		for next := source; ; {
			if seen[next] {
				return errors.New("cyclic model mapping")
			}
			seen[next] = true
			rule, ok := value.Rules[next]
			if !ok || rule.UpstreamID == next {
				break
			}
			next = rule.UpstreamID
		}
	}
	return nil
}

func IDs(raw []byte) ([]string, error) {
	var payload struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(payload.Data))
	for _, row := range payload.Data {
		ids = append(ids, row.ID)
	}
	return ids, nil
}
