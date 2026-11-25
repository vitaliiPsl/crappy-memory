package source_test

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/vitaliipsl/crappy-memory/retrieval"
	"github.com/vitaliipsl/crappy-memory/source"
)

func TestItemsPagesThroughChunks(t *testing.T) {
	t.Parallel()

	chunks := []source.Chunk{
		{ID: "a", DocumentID: "notes", Scope: "project", Position: 1, Location: "line 1", Content: "first"},
		{ID: "b", DocumentID: "chat", Scope: "personal", Content: "second"},
		{ID: "c", DocumentID: "notes", Scope: "project", Content: "third"},
	}
	pages := []source.ChunkPage{{Chunks: chunks[:2], NextID: "b"}, {Chunks: chunks[2:]}}

	var reads []source.ChunkFilter

	store := newChunkStore(func(_ context.Context, filter source.ChunkFilter) (source.ChunkPage, error) {
		reads = append(reads, filter)

		return pages[len(reads)-1], nil
	})

	var items []retrieval.Item

	for item, err := range source.Items(t.Context(), store) {
		if err != nil {
			t.Fatal(err)
		}

		items = append(items, item)
	}

	want := []retrieval.Item{
		{ID: "a", Scope: "project", Version: 1, Text: "first"},
		{ID: "b", Scope: "personal", Version: 1, Text: "second"},
		{ID: "c", Scope: "project", Version: 1, Text: "third"},
	}
	if !slices.Equal(items, want) {
		t.Fatalf("items = %+v, want %+v", items, want)
	}

	if !slices.EqualFunc(reads, []source.ChunkFilter{{Limit: source.DefaultPageSize}, {AfterID: "b", Limit: source.DefaultPageSize}}, func(got, want source.ChunkFilter) bool {
		return got.AfterID == want.AfterID && got.Limit == want.Limit && len(got.DocumentIDs) == 0 && len(got.IDs) == 0
	}) {
		t.Fatalf("store reads = %+v", reads)
	}
}

func TestItemsEndsAtStoreFailure(t *testing.T) {
	t.Parallel()

	failure := errors.New("store unavailable")
	store := newChunkStore(func(_ context.Context, filter source.ChunkFilter) (source.ChunkPage, error) {
		if filter.AfterID == "" {
			return source.ChunkPage{Chunks: []source.Chunk{{ID: "a"}}, NextID: "a"}, nil
		}

		return source.ChunkPage{}, failure
	})

	var (
		ids  []string
		errs []error
	)

	for item, err := range source.Items(t.Context(), store) {
		if err != nil {
			errs = append(errs, err)

			continue
		}

		ids = append(ids, item.ID)
	}

	if !slices.Equal(ids, []string{"a"}) || len(errs) != 1 || !errors.Is(errs[0], failure) {
		t.Fatalf("items = %v, errors = %v", ids, errs)
	}
}

func TestItemsStopsReadingWhenConsumerStops(t *testing.T) {
	t.Parallel()

	reads := 0
	store := newChunkStore(func(context.Context, source.ChunkFilter) (source.ChunkPage, error) {
		reads++

		return source.ChunkPage{Chunks: []source.Chunk{{ID: "a"}, {ID: "b"}}, NextID: "b"}, nil
	})

	for range source.Items(t.Context(), store) {
		break
	}

	if reads != 1 {
		t.Fatalf("store reads after an early stop = %d, want 1", reads)
	}
}

type chunkStore struct {
	source.Store
	chunks func(context.Context, source.ChunkFilter) (source.ChunkPage, error)
}

func newChunkStore(chunks func(context.Context, source.ChunkFilter) (source.ChunkPage, error)) *chunkStore {
	return &chunkStore{chunks: chunks}
}

func (s *chunkStore) Chunks(ctx context.Context, filter source.ChunkFilter) (source.ChunkPage, error) {
	return s.chunks(ctx, filter)
}
