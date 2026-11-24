package inmemory

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"maps"
	"math"
	"slices"
	"strings"
	"sync"

	"github.com/vitaliipsl/crappy-memory/index/vector"
	"github.com/vitaliipsl/crappy-memory/retrieval"
)

var _ vector.Store = (*Store)(nil)

type Option func(*Store)

func WithSimilarity(similarity vector.Similarity) Option {
	return func(store *Store) {
		store.similarity = similarity
	}
}

type Store struct {
	similarity vector.Similarity

	mu      sync.RWMutex
	records map[string]vector.Record
}

func New(options ...Option) *Store {
	store := &Store{
		similarity: vector.Cosine,
		records:    make(map[string]vector.Record),
	}
	for _, option := range options {
		option(store)
	}

	return store
}

func (s *Store) Get(ctx context.Context, id string) (vector.Record, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if err := ctx.Err(); err != nil {
		return vector.Record{}, err
	}

	record, found := s.records[id]
	if !found {
		return vector.Record{}, vector.ErrNotFound
	}

	return copyRecord(record), nil
}

func (s *Store) List(ctx context.Context, afterID string, limit int) (vector.Page, error) {
	if limit <= 0 {
		return vector.Page{}, errors.New("vector list limit must be positive")
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	if err := ctx.Err(); err != nil {
		return vector.Page{}, err
	}

	page := vector.Page{Records: make([]vector.Record, 0, min(limit, len(s.records)))}
	for _, id := range slices.Sorted(maps.Keys(s.records)) {
		if id <= afterID {
			continue
		}

		if len(page.Records) == limit {
			page.NextID = page.Records[len(page.Records)-1].ID

			break
		}

		page.Records = append(page.Records, copyRecord(s.records[id]))
	}

	return page, nil
}

func (s *Store) Upsert(ctx context.Context, records []vector.Record) error {
	prepared := make([]vector.Record, len(records))
	for position, record := range records {
		if err := ctx.Err(); err != nil {
			return err
		}

		if record.ID == "" || record.Version == 0 {
			return errors.New("vector record ID and version are required")
		}

		if strings.TrimSpace(record.Space) == "" {
			return errors.New("vector record space is required")
		}

		if err := validateVector(record.Values); err != nil {
			return fmt.Errorf("vector %q: %w", record.ID, err)
		}

		prepared[position] = copyRecord(record)
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if err := ctx.Err(); err != nil {
		return err
	}

	for _, record := range prepared {
		s.records[record.ID] = record
	}

	return nil
}

func (s *Store) Remove(ctx context.Context, id string) error {
	if id == "" {
		return errors.New("removed vector ID is required")
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if err := ctx.Err(); err != nil {
		return err
	}

	delete(s.records, id)

	return nil
}

func (s *Store) Search(ctx context.Context, query vector.Query) ([]retrieval.Candidate, error) {
	if strings.TrimSpace(query.Space) == "" {
		return nil, errors.New("query space is required")
	}

	if query.Limit < 0 {
		return nil, errors.New("vector search limit cannot be negative")
	}

	if err := validateVector(query.Values); err != nil {
		return nil, fmt.Errorf("query vector: %w", err)
	}

	limit := query.Limit
	if limit == 0 {
		limit = retrieval.DefaultSearchLimit
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	if err := ctx.Err(); err != nil {
		return nil, err
	}

	filter := retrieval.Query{
		Scopes: query.Scopes,
		IDs:    query.IDs,
	}

	candidates := make([]retrieval.Candidate, 0, len(s.records))
	for _, record := range s.records {
		if err := ctx.Err(); err != nil {
			return nil, err
		}

		if record.Space != query.Space || !filter.MatchesFilters(record.ID, record.Scope) {
			continue
		}

		if len(query.Values) != len(record.Values) {
			return nil, fmt.Errorf("vector %q has %d dimensions, query has %d", record.ID, len(record.Values), len(query.Values))
		}

		score := s.similarity(query.Values, record.Values)
		if math.IsNaN(score) || math.IsInf(score, 0) {
			return nil, fmt.Errorf("vector %q similarity score must be finite", record.ID)
		}

		candidates = append(candidates, retrieval.Candidate{ID: record.ID, Version: record.Version, Score: score})
	}

	slices.SortFunc(candidates, func(a, b retrieval.Candidate) int {
		if order := cmp.Compare(b.Score, a.Score); order != 0 {
			return order
		}

		return strings.Compare(a.ID, b.ID)
	})

	return candidates[:min(limit, len(candidates))], nil
}

func validateVector(values []float32) error {
	var squaredNorm float64
	for _, value := range values {
		coordinate := float64(value)
		if math.IsNaN(coordinate) || math.IsInf(coordinate, 0) {
			return errors.New("vector coordinates must be finite")
		}

		squaredNorm += coordinate * coordinate
	}

	if squaredNorm == 0 {
		return errors.New("vector must be non-zero")
	}

	return nil
}

func copyRecord(record vector.Record) vector.Record {
	record.Values = slices.Clone(record.Values)

	return record
}
