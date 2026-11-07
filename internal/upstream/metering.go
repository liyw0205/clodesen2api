package upstream

import (
	"bytes"
	"context"
	"io"

	"clodesen2api/internal/admission"
)

// usageBody retains only a bounded SSE line and the final usage object.
type usageBody struct {
	io.ReadCloser
	ctx      context.Context
	line     []byte
	usage    []byte
	overflow bool
	discard  bool
}

func (b *usageBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	for _, c := range p[:n] {
		if c == '\n' {
			b.consume()
			continue
		}
		if len(b.line) >= 2<<20 {
			b.overflow = true
			b.line = b.line[:0]
		}
		if !b.overflow {
			b.line = append(b.line, c)
		}
	}
	return n, err
}
func (b *usageBody) consume() {
	if !b.overflow && bytes.HasPrefix(b.line, []byte("data:")) {
		payload := bytes.TrimSpace(b.line[5:])
		if bytes.Contains(payload, []byte(`"usage"`)) {
			b.usage = append(b.usage[:0], payload...)
		}
	}
	b.line = b.line[:0]
	b.overflow = false
}
func (b *usageBody) Close() error {
	if len(b.line) > 0 {
		b.consume()
	}
	if !b.discard {
		admission.ReportUsage(b.ctx, b.usage)
	}
	return b.ReadCloser.Close()
}
