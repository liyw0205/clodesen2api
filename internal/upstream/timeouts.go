package upstream

import (
	"context"
	"errors"
	"io"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"clodesen2api/internal/admission"
)

type attemptBudgetKey struct{}

func consumeAttempt(ctx context.Context) error {
	if remaining, ok := ctx.Value(attemptBudgetKey{}).(*atomic.Int32); ok && remaining.Add(-1) < 0 {
		return errors.New("upstream attempt budget exhausted")
	}
	return nil
}

func refundAttempt(ctx context.Context) {
	if remaining, ok := ctx.Value(attemptBudgetKey{}).(*atomic.Int32); ok {
		remaining.Add(1)
	}
}

const maxBufferedResponse = 128 << 20

func readResponse(body io.Reader) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(body, maxBufferedResponse+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxBufferedResponse {
		return nil, errors.New("upstream response exceeds buffer limit")
	}
	return data, nil
}

type timedBody struct {
	io.ReadCloser
	mu       sync.Mutex
	timer    *time.Timer
	cancel   context.CancelFunc
	deadline time.Time
	idle     time.Duration
	expired  bool
	closed   bool
}

func (b *timedBody) expire() {
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return
	}
	if wait := time.Until(b.deadline); wait > 0 {
		b.timer.Reset(wait)
		b.mu.Unlock()
		return
	}
	b.expired = true
	b.cancel()
	b.mu.Unlock()
}
func (b *timedBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	b.mu.Lock()
	if b.expired {
		err = context.DeadlineExceeded
	} else if n > 0 && !b.closed {
		b.deadline = time.Now().Add(b.idle)
		b.timer.Reset(b.idle)
	}
	b.mu.Unlock()
	return n, err
}
func (b *timedBody) Close() error {
	b.mu.Lock()
	b.closed = true
	b.timer.Stop()
	b.cancel()
	b.mu.Unlock()
	return b.ReadCloser.Close()
}
func timedDo(client *http.Client, req *http.Request) (*http.Response, error) {
	limits := admission.Limits(req.Context())
	ctx, cancel := context.WithCancel(req.Context())
	first := time.Duration(limits.FirstByteTimeoutSeconds) * time.Second
	b := &timedBody{cancel: cancel, deadline: time.Now().Add(first), idle: time.Duration(limits.IdleTimeoutSeconds) * time.Second}
	b.timer = time.AfterFunc(first, b.expire)
	resp, err := client.Do(req.Clone(ctx))
	if err != nil {
		b.mu.Lock()
		b.closed = true
		b.timer.Stop()
		cancel()
		if b.expired {
			err = context.DeadlineExceeded
		}
		b.mu.Unlock()
		return nil, err
	}
	b.ReadCloser = resp.Body
	resp.Body = b
	return resp, nil
}
