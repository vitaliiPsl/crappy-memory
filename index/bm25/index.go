package bm25

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"iter"
	"math"
	"slices"
	"strings"
	"sync"
	"unicode"

	"github.com/vitaliipsl/crappy-memory/retrieval"
)

var (
	_ retrieval.Indexer   = (*Index)(nil)
	_ retrieval.Retriever = (*Index)(nil)
)

const (
	defaultSaturation          = 1.2
	defaultLengthNormalization = 0.75
)

type Option func(*Index)

// WithSaturation sets k1; zero makes repeated occurrences contribute no extra weight.
func WithSaturation(k1 float64) Option {
	return func(index *Index) {
		index.k1 = k1
	}
}

// WithLengthNormalization sets b in [0, 1]; zero disables length normalization.
func WithLengthNormalization(b float64) Option {
	return func(index *Index) {
		index.b = b
	}
}

type Index struct {
	k1 float64
	b  float64

	mu          sync.RWMutex
	documents   map[string]document
	postings    map[string]map[string]struct{}
	totalLength int
}

type document struct {
	scope   string
	version uint64
	terms   map[string]int
	length  int
}

func New(items iter.Seq2[retrieval.Item, error], options ...Option) (*Index, error) {
	index := &Index{
		k1:        defaultSaturation,
		b:         defaultLengthNormalization,
		documents: make(map[string]document),
		postings:  make(map[string]map[string]struct{}),
	}
	for _, option := range options {
		option(index)
	}

	if math.IsNaN(index.k1) || math.IsInf(index.k1, 0) || index.k1 < 0 {
		return nil, fmt.Errorf("BM25 saturation must be finite and non-negative: %g", index.k1)
	}

	if math.IsNaN(index.b) || index.b < 0 || index.b > 1 {
		return nil, fmt.Errorf("BM25 length normalization must be in [0, 1]: %g", index.b)
	}

	for item, err := range items {
		if err != nil {
			return nil, fmt.Errorf("load BM25 items: %w", err)
		}

		doc, err := newDocument(item)
		if err != nil {
			return nil, fmt.Errorf("index item %q: %w", item.ID, err)
		}

		index.put(item.ID, doc)
	}

	return index, nil
}

func (i *Index) Upsert(ctx context.Context, item retrieval.Item) error {
	doc, err := newDocument(item)
	if err != nil {
		return err
	}

	i.mu.Lock()
	defer i.mu.Unlock()

	if err := ctx.Err(); err != nil {
		return err
	}

	i.put(item.ID, doc)

	return nil
}

func (i *Index) Remove(ctx context.Context, id string) error {
	if id == "" {
		return errors.New("removed item ID is required")
	}

	i.mu.Lock()
	defer i.mu.Unlock()

	if err := ctx.Err(); err != nil {
		return err
	}

	i.removeDocument(id)

	return nil
}

// Retrieve returns higher BM25 scores first, with item IDs breaking ties.
func (i *Index) Retrieve(ctx context.Context, query retrieval.Query) ([]retrieval.Candidate, error) {
	if query.Limit < 0 {
		return nil, errors.New("retrieval limit cannot be negative")
	}

	limit := query.Limit
	if limit == 0 {
		limit = retrieval.DefaultSearchLimit
	}

	terms := tokenize(query.Text)
	slices.Sort(terms)
	terms = slices.Compact(terms)

	i.mu.RLock()
	defer i.mu.RUnlock()

	if err := ctx.Err(); err != nil {
		return nil, err
	}

	if len(i.documents) == 0 {
		return []retrieval.Candidate{}, nil
	}

	documentCount := float64(len(i.documents))
	averageLength := float64(i.totalLength) / documentCount

	frequencyWeight := 1 / (i.k1 + 1)
	normalizationWeight := i.k1 / (i.k1 + 1)

	scores := make(map[string]float64)
	for _, term := range terms {
		postings := i.postings[term]

		documentFrequency := float64(len(postings))

		idf := math.Log1p((documentCount - documentFrequency + 0.5) / (documentFrequency + 0.5))
		for id := range postings {
			if err := ctx.Err(); err != nil {
				return nil, err
			}

			doc := i.documents[id]
			if !query.MatchesFilters(id, doc.scope) {
				continue
			}

			normalization := 1 - i.b + i.b*float64(doc.length)/averageLength
			tf := float64(doc.terms[term])
			scores[id] += idf * (tf / (tf*frequencyWeight + normalizationWeight*normalization))
		}
	}

	candidates := make([]retrieval.Candidate, 0, len(scores))
	for id, score := range scores {
		candidates = append(candidates, retrieval.Candidate{ID: id, Version: i.documents[id].version, Score: score})
	}

	slices.SortFunc(candidates, func(a, b retrieval.Candidate) int {
		if order := cmp.Compare(b.Score, a.Score); order != 0 {
			return order
		}

		return strings.Compare(a.ID, b.ID)
	})

	return candidates[:min(limit, len(candidates))], nil
}

func (i *Index) put(id string, doc document) {
	if current, found := i.documents[id]; found && current.version >= doc.version {
		return
	}

	i.removeDocument(id)
	i.documents[id] = doc
	i.totalLength += doc.length

	for term := range doc.terms {
		if i.postings[term] == nil {
			i.postings[term] = make(map[string]struct{})
		}

		i.postings[term][id] = struct{}{}
	}
}

func (i *Index) removeDocument(id string) {
	doc := i.documents[id]
	for term := range doc.terms {
		delete(i.postings[term], id)

		if len(i.postings[term]) == 0 {
			delete(i.postings, term)
		}
	}

	i.totalLength -= doc.length
	delete(i.documents, id)
}

func newDocument(item retrieval.Item) (document, error) {
	if item.ID == "" || item.Version == 0 {
		return document{}, errors.New("indexed item ID and version are required")
	}

	terms := tokenize(item.Text)

	doc := document{
		scope:   item.Scope,
		version: item.Version,
		terms:   make(map[string]int),
		length:  len(terms),
	}

	for _, term := range terms {
		doc.terms[term]++
	}

	return doc, nil
}

func tokenize(text string) []string {
	return strings.FieldsFunc(strings.ToLower(text), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsNumber(r)
	})
}
