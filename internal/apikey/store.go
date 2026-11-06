// Package apikey stores and validates client inference keys.
package apikey

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"clodesen2api/internal/admission"
	"clodesen2api/internal/controlstore"
)

const (
	maxKeys       = 1000
	maxNameLength = 64
	maxRPM        = 100000
)

// Key is the non-secret portion of a client key.
type Key struct {
	ID         string           `json:"id"`
	Name       string           `json:"name"`
	Prefix     string           `json:"prefix"`
	Enabled    bool             `json:"enabled"`
	ExpiresAt  *time.Time       `json:"expiresAt,omitempty"`
	IPAllow    []string         `json:"ipAllow,omitempty"`
	RPM        *int             `json:"rpm,omitempty"`
	Models     []string         `json:"models,omitempty"`
	CreatedAt  time.Time        `json:"createdAt"`
	LastUsedAt *time.Time       `json:"lastUsedAt,omitempty"`
	Version    int              `json:"version"`
	Limits     admission.Policy `json:"limits"`
}

type persistedKey struct {
	Key
	SecretHash string `json:"secretHash"`
}

type persistedFile struct {
	Version int            `json:"version"`
	Keys    []persistedKey `json:"keys"`
}

// CreateRequest is the validated shape accepted by the management API.
type CreateRequest struct {
	Name      string           `json:"name"`
	ExpiresAt *time.Time       `json:"expiresAt"`
	IPAllow   []string         `json:"ipAllow"`
	RPM       *int             `json:"rpm"`
	Models    []string         `json:"models"`
	Limits    admission.Policy `json:"limits"`
}

// UpdateRequest uses pointers so omitted fields remain unchanged.
type UpdateRequest struct {
	Version   int               `json:"version"`
	Name      *string           `json:"name"`
	Enabled   *bool             `json:"enabled"`
	ExpiresAt **time.Time       `json:"expiresAt"`
	IPAllow   *[]string         `json:"ipAllow"`
	RPM       **int             `json:"rpm"`
	Models    *[]string         `json:"models"`
	Limits    *admission.Policy `json:"limits"`
}

func (input *UpdateRequest) UnmarshalJSON(data []byte) error {
	type plain UpdateRequest
	var decoded plain
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}
	allowed := map[string]bool{"version": true, "name": true, "enabled": true, "expiresAt": true, "ipAllow": true, "rpm": true, "models": true, "limits": true}
	for field, value := range fields {
		if !allowed[field] {
			return fmt.Errorf("unknown key field %q", field)
		}
		if (field == "name" || field == "enabled" || field == "version") && string(value) == "null" {
			return fmt.Errorf("%s cannot be null", field)
		}
	}
	if raw, ok := fields["expiresAt"]; ok && string(raw) == "null" {
		var empty *time.Time
		decoded.ExpiresAt = &empty
	}
	if raw, ok := fields["rpm"]; ok && string(raw) == "null" {
		var empty *int
		decoded.RPM = &empty
	}
	*input = UpdateRequest(decoded)
	return nil
}

// Principal identifies the key that authorized one request.
type Principal struct {
	ID         string
	Name       string
	Type       string
	Models     []string
	RPM        *int
	Limits     admission.Policy
	secretHash string
	clientIP   string
}

type principalContextKey struct{}

// WithPrincipal attaches the authenticated client identity to a request.
func WithPrincipal(ctx context.Context, principal Principal) context.Context {
	return context.WithValue(ctx, principalContextKey{}, principal)
}

// PrincipalFromContext returns the identity attached by the inference middleware.
func PrincipalFromContext(ctx context.Context) (Principal, bool) {
	principal, ok := ctx.Value(principalContextKey{}).(Principal)
	return principal, ok
}

// Authenticator combines the optional deployment key with managed keys.
// Managed keys are preferred for normal clients; the deployment key is kept
// as a compatibility escape hatch and has no per-key limits.
type Authenticator struct {
	StaticKey        string
	Store            *Store
	AllowNoAuth      bool
	TrustedProxyNets []*net.IPNet
}

func NewAuthenticator(staticKey string, store *Store, allowNoAuth bool, trustedProxies string) (*Authenticator, error) {
	auth := &Authenticator{StaticKey: strings.TrimSpace(staticKey), Store: store, AllowNoAuth: allowNoAuth}
	for _, raw := range strings.Split(trustedProxies, ",") {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		if strings.Contains(raw, "/") {
			_, network, err := net.ParseCIDR(raw)
			if err != nil {
				return nil, fmt.Errorf("invalid trusted proxy %q", raw)
			}
			auth.TrustedProxyNets = append(auth.TrustedProxyNets, network)
			continue
		}
		ip := net.ParseIP(raw)
		if ip == nil {
			return nil, fmt.Errorf("invalid trusted proxy %q", raw)
		}
		bits := 128
		if ip.To4() != nil {
			bits = 32
			ip = ip.To4()
		}
		auth.TrustedProxyNets = append(auth.TrustedProxyNets, &net.IPNet{IP: ip, Mask: net.CIDRMask(bits, bits)})
	}
	return auth, nil
}

// Authenticate extracts the client key and applies the managed-key policy.
func (a *Authenticator) Authenticate(rIP, forwardedFor, authorization, xAPIKey, model string) (Principal, *AuthError) {
	return a.authenticate(rIP, forwardedFor, authorization, xAPIKey, model, true)
}

// PreAuthenticate validates credentials and network policy without consuming
// RPM. Call Authorize after the request body has been parsed.
func (a *Authenticator) PreAuthenticate(rIP, forwardedFor, authorization, xAPIKey string) (Principal, *AuthError) {
	return a.authenticate(rIP, forwardedFor, authorization, xAPIKey, "", false)
}

func (a *Authenticator) authenticate(rIP, forwardedFor, authorization, xAPIKey, model string, countRPM bool) (Principal, *AuthError) {
	token := extractToken(authorization, xAPIKey)
	hasManaged := a.Store != nil && a.Store.HasAny()
	if a.StaticKey == "" && !hasManaged {
		if a.AllowNoAuth {
			return Principal{ID: "anonymous", Name: "匿名", Type: "anonymous"}, nil
		}
		return Principal{}, &AuthError{Status: 401, Code: "api_key_required", Message: "API key is required"}
	}
	if a.StaticKey != "" && subtle.ConstantTimeCompare([]byte(token), []byte(a.StaticKey)) == 1 {
		return Principal{ID: "external", Name: "外部兼容 key", Type: "external"}, nil
	}
	if a.Store == nil {
		return Principal{}, &AuthError{Status: 401, Code: "invalid_api_key", Message: "API key is invalid"}
	}
	remoteIP := a.ClientIP(rIP, forwardedFor)
	return a.Store.authenticate(token, model, remoteIP, countRPM)
}

// Authorize applies model and RPM restrictions for a principal returned by
// PreAuthenticate. Static and anonymous principals have no managed limits.
func (a *Authenticator) Authorize(principal Principal, model string) *AuthError {
	if principal.ID == "" || principal.ID == "external" || principal.ID == "anonymous" || a.Store == nil {
		return nil
	}
	return a.Store.Authorize(principal, model)
}

// Validate checks revocation and network policy without consuming inference RPM.
func (a *Authenticator) Validate(principal Principal) *AuthError {
	if principal.Type != "managed" || a.Store == nil {
		return nil
	}
	return a.Store.authorize(principal, "", false, false)
}

// CheckModel checks the same current policy as inference without consuming RPM.
func (a *Authenticator) CheckModel(principal Principal, model string) *AuthError {
	if principal.Type != "managed" || a.Store == nil {
		return nil
	}
	return a.Store.authorize(principal, model, true, false)
}

// AdmissionCheck runs against the same transaction that admits the request.
// This prevents a concurrent rotation or policy edit from using stale limits.
func (a *Authenticator) AdmissionCheck(principal Principal, model string) admission.Check {
	if principal.Type != "managed" || a.Store == nil || a.Store.control == nil {
		return nil
	}
	return func(read controlstore.Reader) (admission.Policy, *int, error) {
		var saved persistedFile
		if err := read("api-keys", &saved); err != nil {
			return admission.Policy{}, nil, err
		}
		for _, item := range saved.Keys {
			if item.ID != principal.ID {
				continue
			}
			if err := validatePrincipal(&item, principal, model, true); err != nil {
				return admission.Policy{}, nil, err
			}
			return item.Limits, item.RPM, nil
		}
		return admission.Policy{}, nil, &AuthError{Status: 401, Code: "invalid_api_key", Message: "API key is invalid"}
	}
}

func extractToken(authorization, xAPIKey string) string {
	value := strings.TrimSpace(authorization)
	if len(value) >= 7 && strings.EqualFold(value[:7], "Bearer ") {
		return strings.TrimSpace(value[7:])
	}
	return strings.TrimSpace(xAPIKey)
}

func (a *Authenticator) trusted(raw string) bool {
	ip := net.ParseIP(strings.TrimSpace(raw))
	if ip == nil {
		return false
	}
	for _, network := range a.TrustedProxyNets {
		if network.Contains(ip) {
			return true
		}
	}
	return false
}

// ClientIP walks the proxy chain from the nearest trusted hop. A client cannot
// bypass an IP policy by prepending a forged address to X-Forwarded-For.
func (a *Authenticator) ClientIP(remote, forwarded string) string {
	if a == nil || !a.trusted(remote) {
		return remote
	}
	parts := strings.Split(forwarded, ",")
	for i := len(parts) - 1; i >= 0; i-- {
		ip := net.ParseIP(strings.TrimSpace(parts[i]))
		if ip == nil {
			return remote
		}
		remote = ip.String()
		if !a.trusted(remote) {
			break
		}
	}
	return remote
}

// AuthError is intentionally structured so the HTTP layer can emit an
// OpenAI-compatible error without depending on this package's transport.
type AuthError struct {
	Status     int
	Code       string
	Message    string
	RetryAfter int
}

func (e *AuthError) Error() string { return e.Message }

// ErrVersionConflict indicates that another admin update won the race.
var ErrVersionConflict = errors.New("version_conflict")

// ErrNotFound indicates an unknown key id.
var ErrNotFound = errors.New("key_not_found")

func ValidateDocument(raw json.RawMessage) error {
	var saved persistedFile
	if err := json.Unmarshal(raw, &saved); err != nil {
		return err
	}
	if saved.Version != 1 || len(saved.Keys) > maxKeys {
		return errors.New("invalid key schema")
	}
	ids, hashes := map[string]bool{}, map[string]bool{}
	for _, item := range saved.Keys {
		if err := validatePersisted(&item); err != nil {
			return err
		}
		if ids[item.ID] || hashes[item.SecretHash] {
			return errors.New("duplicate key")
		}
		ids[item.ID], hashes[item.SecretHash] = true, true
	}
	return nil
}

// Store is a small owner-only JSON key store. Secrets are never persisted.
type Store struct {
	mu       sync.RWMutex
	path     string
	keys     map[string]*persistedKey
	index    map[string]*persistedKey
	windows  map[string][]time.Time
	dirty    bool
	control  *controlstore.Store
	revision int64
}

// OpenControl imports a validated legacy file once and uses SQLite thereafter.
func OpenControl(control *controlstore.Store, legacy string) (*Store, error) {
	s := &Store{path: legacy, control: control, keys: make(map[string]*persistedKey), index: make(map[string]*persistedKey), windows: make(map[string][]time.Time)}
	ctx := context.Background()
	if _, err := control.Revision(ctx, "api-keys"); errors.Is(err, controlstore.ErrMissing) {
		saved := persistedFile{Version: 1, Keys: []persistedKey{}}
		if _, err := os.Stat(legacy); err == nil {
			old, err := Open(legacy)
			if err != nil {
				return nil, err
			}
			for _, item := range old.keys {
				saved.Keys = append(saved.Keys, *item)
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
		if err := control.Import(ctx, "api-keys", legacy, saved); err != nil {
			return nil, err
		}
	} else if err != nil {
		return nil, err
	}
	if err := s.syncLocked(); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *Store) Sync() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.syncLocked()
}

func (s *Store) syncLocked() error {
	if s.control == nil {
		return nil
	}
	ctx := context.Background()
	revision, err := s.control.Revision(ctx, "api-keys")
	if err != nil {
		return err
	}
	if revision == s.revision {
		return nil
	}
	var saved persistedFile
	revision, err = s.control.Load(ctx, "api-keys", &saved)
	if err != nil {
		return err
	}
	if saved.Version != 1 || len(saved.Keys) > maxKeys {
		return errors.New("invalid API key store version or size")
	}
	keys, index := make(map[string]*persistedKey), make(map[string]*persistedKey)
	for _, item := range saved.Keys {
		item := item
		if err := validatePersisted(&item); err != nil {
			return err
		}
		if keys[item.ID] != nil || index[item.SecretHash] != nil {
			return errors.New("duplicate API key")
		}
		if old := s.keys[item.ID]; old != nil && old.LastUsedAt != nil && (item.LastUsedAt == nil || old.LastUsedAt.After(*item.LastUsedAt)) {
			item.LastUsedAt = cloneTime(old.LastUsedAt)
		}
		keys[item.ID], index[item.SecretHash] = &item, &item
	}
	s.keys, s.index, s.revision = keys, index, revision
	return nil
}

func Open(path string) (*Store, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil, errors.New("API key file path is empty")
	}
	s := &Store{path: path, keys: make(map[string]*persistedKey), index: make(map[string]*persistedKey), windows: make(map[string][]time.Time)}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		if err := s.writeLocked(); err != nil {
			return nil, err
		}
		return s, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read API key file: %w", err)
	}
	var saved persistedFile
	if err := json.Unmarshal(data, &saved); err != nil {
		return nil, fmt.Errorf("decode API key file: %w", err)
	}
	if saved.Version != 1 {
		return nil, fmt.Errorf("unsupported API key file version %d", saved.Version)
	}
	if len(saved.Keys) > maxKeys {
		return nil, fmt.Errorf("API key file contains more than %d keys", maxKeys)
	}
	for i := range saved.Keys {
		item := saved.Keys[i]
		if err := validatePersisted(&item); err != nil {
			return nil, fmt.Errorf("invalid API key %q: %w", item.ID, err)
		}
		if _, exists := s.keys[item.ID]; exists {
			return nil, fmt.Errorf("duplicate API key id %q", item.ID)
		}
		if _, exists := s.index[item.SecretHash]; exists {
			return nil, errors.New("duplicate API key hash")
		}
		copyItem := item
		s.keys[item.ID] = &copyItem
		s.index[item.SecretHash] = &copyItem
	}
	if err := os.Chmod(path, 0o600); err != nil {
		return nil, fmt.Errorf("restrict API key file permissions: %w", err)
	}
	return s, nil
}

func (s *Store) Path() string { return s.path }

func (s *Store) HasAny() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.syncLocked(); err != nil {
		return true
	}
	return len(s.keys) > 0
}

func (s *Store) List() []Key {
	result, _ := s.ListChecked()
	return result
}

func (s *Store) ListChecked() ([]Key, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.syncLocked(); err != nil {
		return nil, err
	}
	result := make([]Key, 0, len(s.keys))
	for _, item := range s.keys {
		result = append(result, clonePublic(item.Key))
	}
	sort.Slice(result, func(i, j int) bool { return result[i].CreatedAt.Before(result[j].CreatedAt) })
	return result, nil
}

func (s *Store) Get(id string) (Key, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.syncLocked(); err != nil {
		return Key{}, err
	}
	item, ok := s.keys[strings.TrimSpace(id)]
	if !ok {
		return Key{}, ErrNotFound
	}
	return clonePublic(item.Key), nil
}

func (s *Store) Create(input CreateRequest) (Key, string, error) {
	if err := validateCreate(input); err != nil {
		return Key{}, "", err
	}
	secret, err := newSecret()
	if err != nil {
		return Key{}, "", err
	}
	id, err := newID()
	if err != nil {
		return Key{}, "", err
	}
	now := time.Now().UTC()
	item := &persistedKey{Key: Key{
		ID: id, Name: strings.TrimSpace(input.Name), Prefix: secret[:8], Enabled: true,
		ExpiresAt: cloneTime(input.ExpiresAt), IPAllow: normalizeNetworks(input.IPAllow),
		RPM: cloneInt(input.RPM), Models: normalizeModels(input.Models), CreatedAt: now, Version: 1, Limits: input.Limits,
	}, SecretHash: hash(secret)}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.syncLocked(); err != nil {
		return Key{}, "", err
	}
	if len(s.keys) >= maxKeys {
		return Key{}, "", fmt.Errorf("最多支持 %d 个 API key", maxKeys)
	}
	s.keys[item.ID] = item
	s.index[item.SecretHash] = item
	if err := s.writeLocked(); err != nil {
		delete(s.keys, item.ID)
		delete(s.index, item.SecretHash)
		return Key{}, "", err
	}
	return clonePublic(item.Key), secret, nil
}

func (s *Store) Update(id string, version int, input UpdateRequest) (Key, error) {
	id = strings.TrimSpace(id)
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.syncLocked(); err != nil {
		return Key{}, err
	}
	item, ok := s.keys[strings.TrimSpace(id)]
	if !ok {
		return Key{}, ErrNotFound
	}
	if version != item.Version {
		return Key{}, ErrVersionConflict
	}
	next := *item
	next.Key = clonePublic(item.Key)
	if input.Name != nil {
		next.Name = strings.TrimSpace(*input.Name)
	}
	if input.Enabled != nil {
		next.Enabled = *input.Enabled
	}
	if input.ExpiresAt != nil {
		next.ExpiresAt = cloneTime(*input.ExpiresAt)
	}
	if input.IPAllow != nil {
		next.IPAllow = normalizeNetworks(*input.IPAllow)
	}
	if input.RPM != nil {
		next.RPM = cloneInt(*input.RPM)
	}
	if input.Models != nil {
		next.Models = normalizeModels(*input.Models)
	}
	if input.Limits != nil {
		next.Limits = *input.Limits
	}
	if err := validateKey(&next.Key); err != nil {
		return Key{}, err
	}
	next.Version++
	s.keys[id] = &next
	s.index[next.SecretHash] = &next
	if err := s.writeLocked(); err != nil {
		s.keys[id] = item
		s.index[item.SecretHash] = item
		return Key{}, err
	}
	return clonePublic(next.Key), nil
}

func (s *Store) Rotate(id string, version int) (Key, string, error) {
	id = strings.TrimSpace(id)
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.syncLocked(); err != nil {
		return Key{}, "", err
	}
	item, ok := s.keys[strings.TrimSpace(id)]
	if !ok {
		return Key{}, "", ErrNotFound
	}
	if version != item.Version {
		return Key{}, "", ErrVersionConflict
	}
	secret, err := newSecret()
	if err != nil {
		return Key{}, "", err
	}
	oldHash, oldPrefix, oldVersion := item.SecretHash, item.Prefix, item.Version
	item.SecretHash, item.Prefix, item.Version = hash(secret), secret[:8], oldVersion+1
	delete(s.index, oldHash)
	s.index[item.SecretHash] = item
	if err := s.writeLocked(); err != nil {
		item.SecretHash, item.Prefix, item.Version = oldHash, oldPrefix, oldVersion
		delete(s.index, hash(secret))
		s.index[oldHash] = item
		return Key{}, "", err
	}
	return clonePublic(item.Key), secret, nil
}

func (s *Store) Delete(id string, version int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.syncLocked(); err != nil {
		return err
	}
	id = strings.TrimSpace(id)
	item, ok := s.keys[id]
	if !ok {
		return ErrNotFound
	}
	if version != item.Version {
		return ErrVersionConflict
	}
	delete(s.keys, id)
	delete(s.index, item.SecretHash)
	window := s.windows[id]
	delete(s.windows, id)
	if err := s.writeLocked(); err != nil {
		s.keys[id] = item
		s.index[item.SecretHash] = item
		s.windows[id] = window
		return err
	}
	return nil
}

// Authenticate validates one bearer/X-API-Key token and applies its limits.
func (s *Store) Authenticate(token, model, remoteIP string) (Principal, *AuthError) {
	return s.authenticate(token, model, remoteIP, true)
}

func (s *Store) authenticate(token, model, remoteIP string, countRPM bool) (Principal, *AuthError) {
	token = strings.TrimSpace(token)
	if token == "" {
		return Principal{}, &AuthError{Status: 401, Code: "api_key_required", Message: "API key is required"}
	}
	digest := hash(token)
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.syncLocked(); err != nil {
		return Principal{}, &AuthError{Status: 503, Code: "key_store_unavailable", Message: "API key store is unavailable", RetryAfter: 3}
	}
	item := s.index[digest]
	if item != nil && subtle.ConstantTimeCompare([]byte(item.SecretHash), []byte(digest)) != 1 {
		item = nil
	}
	if item == nil {
		return Principal{}, &AuthError{Status: 401, Code: "invalid_api_key", Message: "API key is invalid"}
	}
	principal := Principal{ID: item.ID, Name: item.Name, Type: "managed", Models: append([]string(nil), item.Models...), RPM: cloneInt(item.RPM), Limits: item.Limits, secretHash: item.SecretHash, clientIP: remoteIP}
	if !item.Enabled {
		return principal, &AuthError{Status: 401, Code: "api_key_disabled", Message: "API key is disabled"}
	}
	if item.ExpiresAt != nil && !item.ExpiresAt.After(time.Now()) {
		return principal, &AuthError{Status: 401, Code: "api_key_expired", Message: "API key has expired"}
	}
	if len(item.IPAllow) > 0 && !ipAllowed(remoteIP, item.IPAllow) {
		return principal, &AuthError{Status: 403, Code: "ip_not_allowed", Message: "client IP is not allowed for this API key"}
	}
	if strings.TrimSpace(model) != "" && len(item.Models) > 0 && !modelAllowed(model, item.Models) {
		return principal, &AuthError{Status: 403, Code: "model_not_allowed", Message: "model is not allowed for this API key"}
	}
	if countRPM && item.RPM != nil && s.control != nil {
		if err := s.consumeSharedRPM(item); err != nil {
			return principal, err
		}
	}
	if countRPM && item.RPM != nil && s.control == nil {
		now := time.Now()
		queue := s.windows[item.ID]
		cutoff := now.Add(-time.Minute)
		kept := queue[:0]
		for _, stamp := range queue {
			if stamp.After(cutoff) {
				kept = append(kept, stamp)
			}
		}
		if len(kept) >= *item.RPM {
			retry := int(time.Until(kept[0].Add(time.Minute)).Seconds()) + 1
			if retry < 1 {
				retry = 1
			}
			s.windows[item.ID] = kept
			return principal, &AuthError{Status: 429, Code: "rpm_exceeded", Message: "API key request rate limit exceeded", RetryAfter: retry}
		}
		s.windows[item.ID] = append(kept, now)
	}
	now := time.Now().UTC()
	item.LastUsedAt = &now
	s.dirty = true
	return principal, nil
}

// Authorize applies model and rate limits after the request has been parsed.
// Authentication itself can therefore happen before an untrusted body is read.
func (s *Store) Authorize(principal Principal, model string) *AuthError {
	return s.authorize(principal, model, true, true)
}

func (s *Store) authorize(principal Principal, model string, inference, consumeRPM bool) *AuthError {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.syncLocked(); err != nil {
		return &AuthError{Status: 503, Code: "key_store_unavailable", Message: "API key store is unavailable", RetryAfter: 3}
	}
	item, ok := s.keys[principal.ID]
	if !ok {
		return &AuthError{Status: 401, Code: "invalid_api_key", Message: "API key is invalid"}
	}
	if err := validatePrincipal(item, principal, model, inference); err != nil {
		return err
	}
	if consumeRPM && item.RPM != nil && s.control != nil {
		if err := s.consumeSharedRPM(item); err != nil {
			return err
		}
	}
	if consumeRPM && item.RPM != nil && s.control == nil {
		now := time.Now()
		queue := s.windows[item.ID]
		cutoff := now.Add(-time.Minute)
		kept := queue[:0]
		for _, stamp := range queue {
			if stamp.After(cutoff) {
				kept = append(kept, stamp)
			}
		}
		if len(kept) >= *item.RPM {
			retry := int(time.Until(kept[0].Add(time.Minute)).Seconds()) + 1
			if retry < 1 {
				retry = 1
			}
			s.windows[item.ID] = kept
			return &AuthError{Status: 429, Code: "rpm_exceeded", Message: "API key request rate limit exceeded", RetryAfter: retry}
		}
		s.windows[item.ID] = append(kept, now)
	}
	now := time.Now().UTC()
	item.LastUsedAt = &now
	s.dirty = true
	return nil
}

func validatePrincipal(item *persistedKey, principal Principal, model string, inference bool) *AuthError {
	if principal.secretHash != item.SecretHash {
		return &AuthError{Status: 401, Code: "invalid_api_key", Message: "API key was rotated"}
	}
	if !item.Enabled {
		return &AuthError{Status: 401, Code: "api_key_disabled", Message: "API key is disabled"}
	}
	if item.ExpiresAt != nil && !item.ExpiresAt.After(time.Now()) {
		return &AuthError{Status: 401, Code: "api_key_expired", Message: "API key has expired"}
	}
	if len(item.IPAllow) > 0 && !ipAllowed(principal.clientIP, item.IPAllow) {
		return &AuthError{Status: 403, Code: "ip_not_allowed", Message: "client IP is not allowed for this API key"}
	}
	if inference && len(item.Models) > 0 && !modelAllowed(model, item.Models) {
		return &AuthError{Status: 403, Code: "model_not_allowed", Message: "model is not allowed for this API key"}
	}
	return nil
}

func (s *Store) consumeSharedRPM(item *persistedKey) *AuthError {
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	err := s.control.Mutate(ctx, "rpm-"+item.ID, func(raw json.RawMessage) (any, error) {
		var queue []int64
		if len(raw) > 0 {
			if err := json.Unmarshal(raw, &queue); err != nil {
				return nil, err
			}
		}
		now := time.Now()
		kept := queue[:0]
		for _, stamp := range queue {
			if stamp > now.Add(-time.Minute).UnixNano() {
				kept = append(kept, stamp)
			}
		}
		if len(kept) >= *item.RPM {
			retry := int(time.Until(time.Unix(0, kept[0]).Add(time.Minute)).Seconds()) + 1
			if retry < 1 {
				retry = 1
			}
			return nil, &AuthError{Status: 429, Code: "rpm_exceeded", Message: "API key request rate limit exceeded", RetryAfter: retry}
		}
		return append(kept, now.UnixNano()), nil
	})
	if err == nil {
		return nil
	}
	var authErr *AuthError
	if errors.As(err, &authErr) {
		return authErr
	}
	return &AuthError{Status: 503, Code: "rate_store_unavailable", Message: "rate limit store is unavailable", RetryAfter: 3}
}

// FlushUsage batches last-used timestamps without writing in the request path.
func (s *Store) FlushUsage() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.dirty {
		return nil
	}
	if s.control != nil {
		err := s.control.Mutate(context.Background(), "api-keys", func(raw json.RawMessage) (any, error) {
			var saved persistedFile
			if err := json.Unmarshal(raw, &saved); err != nil {
				return nil, err
			}
			for i := range saved.Keys {
				old := s.keys[saved.Keys[i].ID]
				if old != nil && old.LastUsedAt != nil && (saved.Keys[i].LastUsedAt == nil || old.LastUsedAt.After(*saved.Keys[i].LastUsedAt)) {
					saved.Keys[i].LastUsedAt = cloneTime(old.LastUsedAt)
				}
			}
			return saved, nil
		})
		if err == nil {
			s.dirty = false
		}
		return err
	}
	if err := s.writeLocked(); err != nil {
		return err
	}
	s.dirty = false
	return nil
}

func (s *Store) writeLocked() error {
	if s.control != nil {
		saved := persistedFile{Version: 1, Keys: make([]persistedKey, 0, len(s.keys))}
		for _, item := range s.keys {
			saved.Keys = append(saved.Keys, *item)
		}
		revision, err := s.control.Save(context.Background(), "api-keys", s.revision, saved)
		if errors.Is(err, controlstore.ErrConflict) {
			return ErrVersionConflict
		}
		if err == nil {
			s.revision = revision
		}
		return err
	}
	dir := filepath.Dir(s.path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create API key directory: %w", err)
	}
	tmp, err := os.CreateTemp(dir, ".api-keys-*.tmp")
	if err != nil {
		return fmt.Errorf("create API key file: %w", err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	items := make([]persistedKey, 0, len(s.keys))
	for _, item := range s.keys {
		items = append(items, *item)
	}
	sort.Slice(items, func(i, j int) bool { return items[i].CreatedAt.Before(items[j].CreatedAt) })
	if err := json.NewEncoder(tmp).Encode(persistedFile{Version: 1, Keys: items}); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("encode API key file: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpPath, s.path); err != nil {
		return fmt.Errorf("replace API key file: %w", err)
	}
	return os.Chmod(s.path, 0o600)
}

func validatePersisted(item *persistedKey) error {
	if item.SecretHash == "" || len(item.SecretHash) != 64 {
		return errors.New("secret hash is invalid")
	}
	if _, err := hex.DecodeString(item.SecretHash); err != nil {
		return errors.New("secret hash is invalid")
	}
	if item.ID == "" || item.Prefix == "" {
		return errors.New("key identity is invalid")
	}
	return validateKey(&item.Key)
}

func validateCreate(input CreateRequest) error {
	item := Key{Name: strings.TrimSpace(input.Name), ExpiresAt: input.ExpiresAt, IPAllow: normalizeNetworks(input.IPAllow), RPM: input.RPM, Models: input.Models, Enabled: true, Version: 1, Limits: input.Limits}
	return validateKey(&item)
}

func validateKey(item *Key) error {
	if err := item.Limits.Validate(); err != nil {
		return err
	}
	if item.Name == "" || utf8.RuneCountInString(item.Name) > maxNameLength || strings.IndexFunc(item.Name, unicode.IsControl) >= 0 {
		return fmt.Errorf("key name must be 1-%d characters", maxNameLength)
	}
	if item.ID != "" && (len(item.ID) < 4 || len(item.ID) > 64 || strings.ContainsAny(item.ID, "\r\n")) {
		return errors.New("key id is invalid")
	}
	if item.Prefix != "" && len(item.Prefix) != 8 {
		return errors.New("key prefix is invalid")
	}
	if item.Version < 1 {
		return errors.New("key version is invalid")
	}
	if item.RPM != nil && (*item.RPM < 1 || *item.RPM > maxRPM) {
		return fmt.Errorf("RPM must be between 1 and %d", maxRPM)
	}
	if len(item.IPAllow) > 64 {
		return errors.New("at most 64 IP networks are allowed")
	}
	for _, entry := range item.IPAllow {
		if _, _, err := net.ParseCIDR(entry); err != nil {
			return fmt.Errorf("invalid IP network %q", entry)
		}
	}
	if len(item.Models) > 256 {
		return errors.New("at most 256 models are allowed")
	}
	seen := make(map[string]struct{}, len(item.Models))
	for _, model := range item.Models {
		model = strings.ToLower(strings.TrimSpace(model))
		if model == "" || len(model) > 200 {
			return errors.New("model list contains an invalid model")
		}
		if _, ok := seen[model]; ok {
			return errors.New("model list contains duplicates")
		}
		seen[model] = struct{}{}
	}
	return nil
}

func newSecret() (string, error) {
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return "sk-" + hex.EncodeToString(raw), nil
}

func newID() (string, error) {
	raw := make([]byte, 12)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return "k_" + hex.EncodeToString(raw), nil
}

func hash(secret string) string {
	digest := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(digest[:])
}

func clonePublic(item Key) Key {
	item.IPAllow = append([]string(nil), item.IPAllow...)
	item.Models = append([]string(nil), item.Models...)
	item.ExpiresAt = cloneTime(item.ExpiresAt)
	item.LastUsedAt = cloneTime(item.LastUsedAt)
	item.RPM = cloneInt(item.RPM)
	return item
}

func cloneTime(value *time.Time) *time.Time {
	if value == nil {
		return nil
	}
	copy := value.UTC()
	return &copy
}

func cloneInt(value *int) *int {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func normalizeStrings(values []string) []string {
	result := make([]string, 0, len(values))
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			result = append(result, value)
		}
	}
	return result
}

func normalizeNetworks(values []string) []string {
	result := normalizeStrings(values)
	for i, value := range result {
		if ip := net.ParseIP(value); ip != nil {
			bits := 128
			if ip.To4() != nil {
				bits = 32
			}
			result[i] = fmt.Sprintf("%s/%d", ip.String(), bits)
		} else if _, network, err := net.ParseCIDR(value); err == nil {
			result[i] = network.String()
		}
	}
	return result
}

func normalizeModels(values []string) []string {
	result := make([]string, 0, len(values))
	for _, value := range values {
		if value = strings.ToLower(strings.TrimSpace(value)); value != "" {
			result = append(result, value)
		}
	}
	return result
}

func modelAllowed(model string, allowed []string) bool {
	model = strings.ToLower(strings.TrimSpace(model))
	for _, candidate := range allowed {
		if model == strings.ToLower(candidate) {
			return true
		}
	}
	return false
}

func ipAllowed(raw string, allowed []string) bool {
	ip := net.ParseIP(strings.TrimSpace(raw))
	if ip == nil {
		return false
	}
	for _, entry := range allowed {
		if _, network, err := net.ParseCIDR(entry); err == nil && network.Contains(ip) {
			return true
		}
	}
	return false
}
