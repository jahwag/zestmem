package embed

import (
	"context"
	"time"

	"github.com/bytelope/zestmem/internal/memory"
)

type EmbeddingObserver interface{ ObserveEmbedding(time.Duration, error) }
type Observed struct {
	next     memory.Embedder
	observer EmbeddingObserver
}

func NewObserved(next memory.Embedder, observer EmbeddingObserver) *Observed {
	return &Observed{next: next, observer: observer}
}
func (o *Observed) Embed(ctx context.Context, text string) (vector []float32, err error) {
	start := time.Now()
	defer func() { o.observer.ObserveEmbedding(time.Since(start), err) }()
	return o.next.Embed(ctx, text)
}
