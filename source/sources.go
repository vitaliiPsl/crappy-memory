package source

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/vitaliipsl/crappy-memory/retrieval"
)

var ErrRetrieverRequired = errors.New("source search requires a retriever")

type SearchResult struct {
	Chunk Chunk   `json:"chunk"`
	Score float64 `json:"score"`
}

type Sources struct {
	// Chunk IDs carry no increasing version, so concurrent writes of one document could
	// remove each other's chunks from an index; writes are serialized instead.
	writes sync.Mutex

	store     Store
	chunkers  map[string]Chunker
	indexers  []indexer
	retriever retrieval.Retriever
}

type indexer struct {
	index   retrieval.Indexer
	onError func(context.Context, error)
}

type Option func(*Sources)

func WithChunker(mediaType string, chunker Chunker) Option {
	return func(s *Sources) {
		s.chunkers[mediaType] = chunker
	}
}

func WithIndexer(index retrieval.Indexer, onError func(context.Context, error)) Option {
	return func(s *Sources) {
		s.indexers = append(s.indexers, indexer{index: index, onError: onError})
	}
}

func WithRetriever(retriever retrieval.Retriever) Option {
	return func(s *Sources) {
		s.retriever = retriever
	}
}

func New(ctx context.Context, store Store, options ...Option) (*Sources, error) {
	sources := &Sources{
		store:    store,
		chunkers: make(map[string]Chunker),
	}
	for _, option := range options {
		option(sources)
	}

	if err := sources.rechunk(ctx); err != nil {
		return nil, err
	}

	return sources, nil
}

func (s *Sources) Put(ctx context.Context, id, scope, mediaType string, content []byte) (Document, error) {
	if strings.TrimSpace(id) == "" || strings.TrimSpace(scope) == "" || strings.TrimSpace(mediaType) == "" {
		return Document{}, errors.New("source document ID, scope and media type are required")
	}

	chunker, found := s.chunkers[mediaType]
	if !found {
		return Document{}, fmt.Errorf("no chunker for media type %q", mediaType)
	}

	s.writes.Lock()
	defer s.writes.Unlock()

	existing, err := s.store.Get(ctx, id)
	if err != nil && !errors.Is(err, ErrNotFound) {
		return Document{}, err
	}

	exists := err == nil
	now := time.Now().UTC()

	document := Document{
		ID:        id,
		Scope:     scope,
		MediaType: mediaType,
		Content:   content,
		Hash:      hash(content),
		Chunker:   chunker.ID(),
		CreatedAt: now,
		UpdatedAt: now,
	}

	if exists {
		if unchanged(existing, document) {
			return existing, nil
		}

		document.CreatedAt = existing.CreatedAt
	}

	if err := s.replace(ctx, document, chunker); err != nil {
		return Document{}, err
	}

	return document, nil
}

func (s *Sources) Delete(ctx context.Context, id string) error {
	s.writes.Lock()
	defer s.writes.Unlock()

	removed, err := s.chunkIDs(ctx, id)
	if err != nil {
		return err
	}

	if err := s.store.Delete(ctx, id); err != nil {
		return err
	}

	for chunkID := range removed {
		s.remove(ctx, chunkID)
	}

	return nil
}

func (s *Sources) Get(ctx context.Context, id string) (Document, error) {
	return s.store.Get(ctx, id)
}

func (s *Sources) List(ctx context.Context, filter Filter) (Page, error) {
	return s.store.List(ctx, filter)
}

func (s *Sources) Search(ctx context.Context, text string, options ...retrieval.QueryOption) ([]SearchResult, error) {
	if s.retriever == nil {
		return nil, ErrRetrieverRequired
	}

	query := retrieval.NewQuery(text, options...)

	candidates, err := s.retriever.Retrieve(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("retrieve source candidates: %w", err)
	}

	if len(candidates) == 0 {
		return []SearchResult{}, nil
	}

	chunks, err := s.load(ctx, candidates)
	if err != nil {
		return nil, err
	}

	results := make([]SearchResult, 0, len(candidates))
	for _, candidate := range candidates {
		chunk, found := chunks[candidate.ID]
		if !found || !query.MatchesFilters(chunk.ID, chunk.Scope) {
			continue
		}

		delete(chunks, candidate.ID)
		results = append(results, SearchResult{
			Chunk: chunk,
			Score: candidate.Score,
		})
	}

	return results, nil
}

func (s *Sources) rechunk(ctx context.Context) error {
	filter := Filter{
		Limit: DefaultPageSize,
	}

	for {
		page, err := s.store.List(ctx, filter)
		if err != nil {
			return fmt.Errorf("list source documents: %w", err)
		}

		for _, document := range page.Documents {
			chunker, found := s.chunkers[document.MediaType]
			if !found || chunker.ID() == document.Chunker {
				continue
			}

			document.Chunker = chunker.ID()
			if err := s.replace(ctx, document, chunker); err != nil {
				return err
			}
		}

		if page.NextID == "" {
			return nil
		}

		filter.AfterID = page.NextID
	}
}

func (s *Sources) replace(ctx context.Context, document Document, chunker Chunker) error {
	passages, err := chunker.Chunk(document.Content)
	if err != nil {
		return fmt.Errorf("chunk document %q: %w", document.ID, err)
	}

	previous, err := s.chunkIDs(ctx, document.ID)
	if err != nil {
		return err
	}

	chunks := newChunks(document, passages)
	if err := s.store.Put(ctx, document, chunks); err != nil {
		return fmt.Errorf("store document %q: %w", document.ID, err)
	}

	current := make(map[string]struct{}, len(chunks))
	for _, chunk := range chunks {
		current[chunk.ID] = struct{}{}
		if _, indexed := previous[chunk.ID]; !indexed {
			s.upsert(ctx, chunk)
		}
	}

	for id := range previous {
		if _, kept := current[id]; !kept {
			s.remove(ctx, id)
		}
	}

	return nil
}

func (s *Sources) chunkIDs(ctx context.Context, documentID string) (map[string]struct{}, error) {
	filter := ChunkFilter{
		DocumentIDs: []string{documentID},
		Limit:       DefaultPageSize,
	}

	ids := make(map[string]struct{})

	for {
		page, err := s.store.Chunks(ctx, filter)
		if err != nil {
			return nil, fmt.Errorf("list chunks of document %q: %w", documentID, err)
		}

		for _, chunk := range page.Chunks {
			ids[chunk.ID] = struct{}{}
		}

		if page.NextID == "" {
			return ids, nil
		}

		filter.AfterID = page.NextID
	}
}

func (s *Sources) load(ctx context.Context, candidates []retrieval.Candidate) (map[string]Chunk, error) {
	ids := make([]string, len(candidates))
	for position, candidate := range candidates {
		ids[position] = candidate.ID
	}

	page, err := s.store.Chunks(ctx, ChunkFilter{IDs: ids, Limit: len(ids)})
	if err != nil {
		return nil, fmt.Errorf("load source candidates: %w", err)
	}

	chunks := make(map[string]Chunk, len(page.Chunks))
	for _, chunk := range page.Chunks {
		chunks[chunk.ID] = chunk
	}

	return chunks, nil
}

func (s *Sources) upsert(ctx context.Context, chunk Chunk) {
	for _, indexer := range s.indexers {
		if err := indexer.index.Upsert(ctx, item(chunk)); err != nil {
			indexer.onError(ctx, fmt.Errorf("index chunk %q of document %q in %T: %w", chunk.ID, chunk.DocumentID, indexer.index, err))
		}
	}
}

func (s *Sources) remove(ctx context.Context, id string) {
	for _, indexer := range s.indexers {
		if err := indexer.index.Remove(ctx, id); err != nil {
			indexer.onError(ctx, fmt.Errorf("remove chunk %q from %T: %w", id, indexer.index, err))
		}
	}
}

func unchanged(existing, document Document) bool {
	return existing.Hash == document.Hash &&
		existing.Scope == document.Scope &&
		existing.MediaType == document.MediaType &&
		existing.Chunker == document.Chunker
}

func newChunks(document Document, passages []Passage) []Chunk {
	occurrences := make(map[string]int, len(passages))

	chunks := make([]Chunk, len(passages))
	for position, passage := range passages {
		chunks[position] = Chunk{
			ID:         chunkID(document, passage.Content, occurrences[passage.Content]),
			DocumentID: document.ID,
			Scope:      document.Scope,
			Position:   position,
			Location:   passage.Location,
			Content:    passage.Content,
		}

		occurrences[passage.Content]++
	}

	return chunks
}

func chunkID(document Document, content string, occurrence int) string {
	digest := sha256.New()
	for _, part := range []string{document.ID, document.Scope, content, strconv.Itoa(occurrence)} {
		_, _ = fmt.Fprintf(digest, "%d:%s", len(part), part)
	}

	return hex.EncodeToString(digest.Sum(nil)[:16])
}

func hash(content []byte) string {
	digest := sha256.Sum256(content)

	return hex.EncodeToString(digest[:])
}
