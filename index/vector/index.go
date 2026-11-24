package vector

import (
	"context"
	"errors"
	"fmt"
	"iter"
	"strings"

	"github.com/vitaliipsl/crappy-memory/retrieval"
)

var (
	_ retrieval.Indexer   = (*Index)(nil)
	_ retrieval.Retriever = (*Index)(nil)
)

const batchSize = 100

type Index struct {
	store    Store
	embedder Embedder
	space    string
}

// New requires stable item and vector stores during reconciliation.
func New(ctx context.Context, items iter.Seq2[retrieval.Item, error], store Store, embedder Embedder) (*Index, error) {
	space := embedder.Space()
	if strings.TrimSpace(space) == "" {
		return nil, errors.New("embedding space is required")
	}

	index := &Index{
		store:    store,
		embedder: embedder,
		space:    space,
	}

	if err := index.reconcile(ctx, items); err != nil {
		return nil, err
	}

	return index, nil
}

func (i *Index) Upsert(ctx context.Context, item retrieval.Item) error {
	record, err := i.store.Get(ctx, item.ID)
	if err != nil && !errors.Is(err, ErrNotFound) {
		return fmt.Errorf("load vector %q: %w", item.ID, err)
	}

	if err == nil && (record.Version > item.Version || i.current(record, item)) {
		return nil
	}

	return i.embed(ctx, []retrieval.Item{item})
}

func (i *Index) Remove(ctx context.Context, id string) error {
	return i.store.Remove(ctx, id)
}

func (i *Index) Retrieve(ctx context.Context, query retrieval.Query) ([]retrieval.Candidate, error) {
	if query.Limit < 0 {
		return nil, errors.New("retrieval limit cannot be negative")
	}

	if err := ctx.Err(); err != nil {
		return nil, err
	}

	if strings.TrimSpace(query.Text) == "" {
		return []retrieval.Candidate{}, nil
	}

	values, err := i.embedder.EmbedQuery(ctx, query.Text)
	if err != nil {
		return nil, fmt.Errorf("embed query: %w", err)
	}

	return i.store.Search(ctx, Query{Space: i.space, Values: values, Scopes: query.Scopes, IDs: query.IDs, Limit: query.Limit})
}

func (i *Index) reconcile(ctx context.Context, items iter.Seq2[retrieval.Item, error]) error {
	indexed := make(map[string]struct{})

	batch := make([]retrieval.Item, 0, batchSize)
	for item, err := range items {
		if err != nil {
			return fmt.Errorf("load vector items: %w", err)
		}

		indexed[item.ID] = struct{}{}

		batch = append(batch, item)
		if len(batch) == batchSize {
			if err := i.refresh(ctx, batch); err != nil {
				return err
			}

			batch = batch[:0]
		}
	}

	if err := i.refresh(ctx, batch); err != nil {
		return err
	}

	afterID := ""
	for {
		page, err := i.store.List(ctx, afterID, batchSize)
		if err != nil {
			return fmt.Errorf("list indexed vectors: %w", err)
		}

		for _, record := range page.Records {
			if _, found := indexed[record.ID]; found {
				continue
			}

			if err := i.store.Remove(ctx, record.ID); err != nil {
				return fmt.Errorf("remove orphaned vector %q: %w", record.ID, err)
			}
		}

		if page.NextID == "" {
			return nil
		}

		afterID = page.NextID
	}
}

func (i *Index) refresh(ctx context.Context, items []retrieval.Item) error {
	pending := make([]retrieval.Item, 0, len(items))
	for _, item := range items {
		record, err := i.store.Get(ctx, item.ID)
		if err != nil && !errors.Is(err, ErrNotFound) {
			return fmt.Errorf("load vector %q: %w", item.ID, err)
		}

		if err == nil && i.current(record, item) {
			continue
		}

		pending = append(pending, item)
	}

	return i.embed(ctx, pending)
}

func (i *Index) current(record Record, item retrieval.Item) bool {
	return record.Version == item.Version && record.Space == i.space
}

func (i *Index) embed(ctx context.Context, items []retrieval.Item) error {
	if len(items) == 0 {
		return nil
	}

	texts := make([]string, len(items))
	for position, item := range items {
		if item.ID == "" || item.Version == 0 {
			return errors.New("indexed item ID and version are required")
		}

		texts[position] = item.Text
	}

	embeddings, err := i.embedder.EmbedDocuments(ctx, texts)
	if err != nil {
		return fmt.Errorf("embed items: %w", err)
	}

	if len(embeddings) != len(items) {
		return fmt.Errorf("embedder returned %d vectors for %d items", len(embeddings), len(items))
	}

	records := make([]Record, len(items))
	for position, item := range items {
		records[position] = Record{
			ID:      item.ID,
			Scope:   item.Scope,
			Version: item.Version,
			Space:   i.space,
			Values:  embeddings[position],
		}
	}

	if err := i.store.Upsert(ctx, records); err != nil {
		return fmt.Errorf("store embedded items: %w", err)
	}

	return nil
}
