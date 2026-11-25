package source

import (
	"context"
	"errors"
	"iter"

	"github.com/vitaliipsl/crappy-memory/retrieval"
)

var ErrNotFound = errors.New("source document not found")

type Store interface {
	Get(ctx context.Context, id string) (Document, error)
	List(ctx context.Context, filter Filter) (Page, error)
	Put(ctx context.Context, document Document, chunks []Chunk) error
	Delete(ctx context.Context, id string) error
	Chunks(ctx context.Context, filter ChunkFilter) (ChunkPage, error)
}

func Items(ctx context.Context, store Store) iter.Seq2[retrieval.Item, error] {
	return func(yield func(retrieval.Item, error) bool) {
		filter := ChunkFilter{
			Limit: DefaultPageSize,
		}

		for {
			page, err := store.Chunks(ctx, filter)
			if err != nil {
				yield(retrieval.Item{}, err)

				return
			}

			for _, chunk := range page.Chunks {
				if !yield(item(chunk), nil) {
					return
				}
			}

			if page.NextID == "" {
				return
			}

			filter.AfterID = page.NextID
		}
	}
}

func item(chunk Chunk) retrieval.Item {
	return retrieval.Item{
		ID:      chunk.ID,
		Scope:   chunk.Scope,
		Version: 1,
		Text:    chunk.Content,
	}
}
