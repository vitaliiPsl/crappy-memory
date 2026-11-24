package memory

import (
	"context"
	"errors"
	"iter"

	"github.com/vitaliipsl/crappy-memory/retrieval"
)

var (
	ErrNotFound      = errors.New("memory entry not found")
	ErrAlreadyExists = errors.New("memory entry already exists")
	ErrConflict      = errors.New("memory entry version conflict")
)

type Store interface {
	Get(ctx context.Context, id string) (Entry, error)
	List(ctx context.Context, filter Filter) (Page, error)
	Create(ctx context.Context, entry Entry) error
	Update(ctx context.Context, entry Entry, expectedVersion uint64) error
	Delete(ctx context.Context, id string, expectedVersion uint64) error
}

func Items(ctx context.Context, store Store) iter.Seq2[retrieval.Item, error] {
	return func(yield func(retrieval.Item, error) bool) {
		filter := Filter{Limit: DefaultPageSize}
		for {
			page, err := store.List(ctx, filter)
			if err != nil {
				yield(retrieval.Item{}, err)

				return
			}

			for _, entry := range page.Entries {
				if !yield(item(entry), nil) {
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

func item(entry Entry) retrieval.Item {
	return retrieval.Item{ID: entry.ID, Scope: entry.Scope, Version: entry.Version, Text: entry.Content}
}
