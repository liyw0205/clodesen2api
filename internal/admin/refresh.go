package admin

import (
	"context"
	"errors"
	"sync"
	"time"

	"clodesen2api/internal/upstream"
)

type refreshCall struct {
	done   chan struct{}
	value  any
	err    error
	cancel context.CancelFunc
}
type refreshGroup struct {
	mu       sync.Mutex
	calls    map[string]*refreshCall
	failures map[string]bool
	partial  map[string][]string
	closed   bool
	wg       sync.WaitGroup
}

func (g *refreshGroup) start(key string, fetch func(context.Context) (any, error)) *refreshCall {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.closed {
		done := make(chan struct{})
		close(done)
		return &refreshCall{done: done, err: errors.New("refresh service closed")}
	}
	if call := g.calls[key]; call != nil {
		return call
	}
	if g.calls == nil {
		g.calls = map[string]*refreshCall{}
	}
	if g.failures == nil {
		g.failures = map[string]bool{}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 35*time.Second)
	call := &refreshCall{done: make(chan struct{}), cancel: cancel}
	g.calls[key] = call
	g.wg.Add(1)
	go func() {
		defer g.wg.Done()
		defer cancel()
		call.value, call.err = fetch(ctx)
		g.mu.Lock()
		g.failures[key] = call.err != nil
		delete(g.calls, key)
		close(call.done)
		g.mu.Unlock()
	}()
	return call
}

func (g *refreshGroup) close() {
	g.mu.Lock()
	g.closed = true
	for _, call := range g.calls {
		call.cancel()
	}
	g.mu.Unlock()
	g.wg.Wait()
}

func (s *Service) Close() { s.refreshes.close() }

func (s *Service) partialRefresh(key string, failures []string) {
	s.refreshes.mu.Lock()
	defer s.refreshes.mu.Unlock()
	if s.refreshes.partial == nil {
		s.refreshes.partial = map[string][]string{}
	}
	s.refreshes.partial[key] = append([]string{}, failures...)
}
func (g *refreshGroup) do(ctx context.Context, key string, fetch func(context.Context) (any, error)) (any, error) {
	call := g.start(key, fetch)
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-call.done:
		return call.value, call.err
	}
}
func (s *Service) refreshModels(ctx context.Context, force bool) ([]byte, error) {
	value, err := s.refreshes.do(ctx, "models", func(ctx context.Context) (any, error) { return s.fetchModels(ctx, force) })
	if err != nil {
		return nil, err
	}
	return value.([]byte), nil
}
func (s *Service) refreshAccounts(ctx context.Context, force bool) ([]byte, error) {
	value, err := s.refreshes.do(ctx, "accounts", func(ctx context.Context) (any, error) { return s.fetchAccounts(ctx, force) })
	if err != nil {
		return nil, err
	}
	return value.([]byte), nil
}
func (s *Service) refreshCreditUsage(ctx context.Context, force bool) ([]upstream.CreditUsageRecord, error) {
	value, err := s.refreshes.do(ctx, "credits", func(ctx context.Context) (any, error) { return s.fetchCreditUsage(ctx, force) })
	if err != nil {
		cached, _, _ := s.creditCacheSnapshot()
		return cached, err
	}
	return value.([]upstream.CreditUsageRecord), nil
}
func (s *Service) cacheStatus() map[string]any {
	s.cacheMu.Lock()
	times := map[string]time.Time{"models": s.modelCachedAt, "accounts": s.accountCachedAt, "credits": s.creditUsageCachedAt}
	s.cacheMu.Unlock()
	s.refreshes.mu.Lock()
	defer s.refreshes.mu.Unlock()
	result := map[string]any{}
	for key, cachedAt := range times {
		age := 0.
		state := "empty"
		if !cachedAt.IsZero() {
			age = time.Since(cachedAt).Seconds()
			state = "hit"
			if age > upstreamCacheTTL.Seconds() {
				state = "stale"
			}
		}
		lastError := ""
		if s.refreshes.failures[key] {
			lastError = "refresh_failed"
			if !cachedAt.IsZero() {
				state = "stale-on-error"
			}
		}
		partial := append([]string{}, s.refreshes.partial[key]...)
		if len(partial) > 0 && lastError == "" {
			state = "partial"
		}
		result[key] = map[string]any{"cacheState": state, "cachedAt": cachedAt, "age": age, "refreshing": s.refreshes.calls[key] != nil, "lastError": lastError, "partialFailures": partial}
	}
	return result
}
