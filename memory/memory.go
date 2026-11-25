package memory

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/vitaliipsl/crappy-memory/retrieval"
)

var ErrRetrieverRequired = errors.New("memory search requires a retriever")

type SearchResult struct {
	Entry Entry   `json:"entry"`
	Score float64 `json:"score"`
}

type Memory struct {
	store     Store
	indexers  []indexer
	retriever retrieval.Retriever
}

type indexer struct {
	index   retrieval.Indexer
	onError func(context.Context, error)
}

type Option func(*Memory)

func WithIndexer(index retrieval.Indexer, onError func(context.Context, error)) Option {
	return func(m *Memory) {
		m.indexers = append(m.indexers, indexer{index: index, onError: onError})
	}
}

func WithRetriever(retriever retrieval.Retriever) Option {
	return func(m *Memory) {
		m.retriever = retriever
	}
}

func New(store Store, options ...Option) *Memory {
	m := &Memory{store: store}
	for _, option := range options {
		option(m)
	}

	return m
}

func (m *Memory) Create(ctx context.Context, scope, content string, options ...EntryOption) (Entry, error) {
	if strings.TrimSpace(scope) == "" || strings.TrimSpace(content) == "" {
		return Entry{}, errors.New("memory scope and content are required")
	}

	now := time.Now().UTC()

	entry := Entry{
		ID:        uuid.Must(uuid.NewV7()).String(),
		Scope:     scope,
		Content:   content,
		CreatedAt: now,
		UpdatedAt: now,
		Version:   1,
	}

	if err := applyEntryOptions(&entry, options); err != nil {
		return Entry{}, err
	}

	if err := m.store.Create(ctx, entry); err != nil {
		return Entry{}, err
	}

	m.upsert(ctx, entry)

	return entry, nil
}

func (m *Memory) Update(ctx context.Context, id string, expectedVersion uint64, content string, options ...EntryOption) (Entry, error) {
	if strings.TrimSpace(content) == "" {
		return Entry{}, errors.New("memory content is required")
	}

	entry, err := m.store.Get(ctx, id)
	if err != nil {
		return Entry{}, err
	}

	if err := applyEntryOptions(&entry, options); err != nil {
		return Entry{}, err
	}

	entry.Content = content
	entry.UpdatedAt = time.Now().UTC()
	entry.Version = expectedVersion + 1

	if err := m.store.Update(ctx, entry, expectedVersion); err != nil {
		return Entry{}, err
	}

	m.upsert(ctx, entry)

	return entry, nil
}

func (m *Memory) Delete(ctx context.Context, id string, expectedVersion uint64) error {
	err := m.store.Delete(ctx, id, expectedVersion)
	if err != nil && !errors.Is(err, ErrNotFound) {
		return err
	}

	for _, indexer := range m.indexers {
		if err := indexer.index.Remove(ctx, id); err != nil {
			indexer.onError(ctx, fmt.Errorf("remove memory %q from %T: %w", id, indexer.index, err))
		}
	}

	return err
}

func (m *Memory) Get(ctx context.Context, id string) (Entry, error) {
	return m.store.Get(ctx, id)
}

func (m *Memory) List(ctx context.Context, filter Filter) (Page, error) {
	return m.store.List(ctx, filter)
}

func (m *Memory) Search(ctx context.Context, text string, options ...retrieval.QueryOption) ([]SearchResult, error) {
	if m.retriever == nil {
		return nil, ErrRetrieverRequired
	}

	query := retrieval.NewQuery(text, options...)

	candidates, err := m.retriever.Retrieve(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("retrieve memory candidates: %w", err)
	}

	if len(candidates) == 0 {
		return []SearchResult{}, nil
	}

	entries, err := m.load(ctx, candidates)
	if err != nil {
		return nil, err
	}

	results := make([]SearchResult, 0, len(candidates))
	for _, candidate := range candidates {
		entry, found := entries[candidate.ID]
		if !found || (candidate.Version != 0 && candidate.Version != entry.Version) || !query.MatchesFilters(entry.ID, entry.Scope) {
			continue
		}

		delete(entries, candidate.ID)
		results = append(results, SearchResult{
			Entry: entry,
			Score: candidate.Score,
		})
	}

	return results, nil
}

func (m *Memory) load(ctx context.Context, candidates []retrieval.Candidate) (map[string]Entry, error) {
	ids := make([]string, len(candidates))
	for position, candidate := range candidates {
		ids[position] = candidate.ID
	}

	page, err := m.store.List(ctx, Filter{IDs: ids, Limit: len(ids)})
	if err != nil {
		return nil, fmt.Errorf("load memory candidates: %w", err)
	}

	entries := make(map[string]Entry, len(page.Entries))
	for _, entry := range page.Entries {
		entries[entry.ID] = entry
	}

	return entries, nil
}

func (m *Memory) upsert(ctx context.Context, entry Entry) {
	for _, indexer := range m.indexers {
		if err := indexer.index.Upsert(ctx, item(entry)); err != nil {
			indexer.onError(ctx, fmt.Errorf("index memory %q at version %d in %T: %w", entry.ID, entry.Version, indexer.index, err))
		}
	}
}
