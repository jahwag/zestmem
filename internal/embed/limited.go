package embed

import (
	"context"
	"errors"

	"github.com/bytelope/zestmem/internal/memory"
)

var ErrBusy = errors.New("embedding queue full; retry later")

type Limited struct {
	next memory.Embedder
	sem  chan struct{}
}

func NewLimited(next memory.Embedder, concurrency int) *Limited {
	if concurrency < 1 {
		concurrency = 1
	}
	return &Limited{next: next, sem: make(chan struct{}, concurrency)}
}

func (l *Limited) Embed(ctx context.Context, text string) ([]float32, error) {
	select {
	case l.sem <- struct{}{}:
		defer func() { <-l.sem }()
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
		return nil, ErrBusy
	}
	return l.next.Embed(ctx, text)
}
