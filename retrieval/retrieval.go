package retrieval

import (
	"context"
	"slices"
)

const DefaultSearchLimit = 10

type Item struct {
	ID      string `json:"id"`
	Scope   string `json:"scope"`
	Version uint64 `json:"version"`
	Text    string `json:"text"`
}

type Indexer interface {
	Upsert(ctx context.Context, item Item) error
	Remove(ctx context.Context, id string) error
}

type Retriever interface {
	Retrieve(ctx context.Context, query Query) ([]Candidate, error)
}

type Merger interface {
	Merge(ctx context.Context, lists [][]Candidate) ([]Candidate, error)
}

type QueryOption func(*Query)

func WithScopes(scopes ...string) QueryOption {
	return func(query *Query) {
		query.Scopes = append([]string(nil), scopes...)
	}
}

func WithIDs(ids ...string) QueryOption {
	return func(query *Query) {
		query.IDs = append([]string(nil), ids...)
	}
}

func WithLimit(limit int) QueryOption {
	return func(query *Query) {
		query.Limit = limit
	}
}

type Query struct {
	Text   string   `json:"text"`
	Scopes []string `json:"scopes,omitempty"`
	IDs    []string `json:"ids,omitempty"`
	Limit  int      `json:"limit,omitempty"`
}

func (q Query) MatchesFilters(id, scope string) bool {
	return (len(q.Scopes) == 0 || slices.Contains(q.Scopes, scope)) &&
		(len(q.IDs) == 0 || slices.Contains(q.IDs, id))
}

func NewQuery(text string, options ...QueryOption) Query {
	query := Query{Text: text}
	for _, option := range options {
		option(&query)
	}

	if query.Limit == 0 {
		query.Limit = DefaultSearchLimit
	}

	return query
}

type Candidate struct {
	ID      string  `json:"id"`
	Version uint64  `json:"version,omitempty"`
	Score   float64 `json:"score"`
}
