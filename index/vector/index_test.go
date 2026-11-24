package vector_test

import (
	"context"
	"errors"
	"fmt"
	"iter"
	"slices"
	"testing"

	"github.com/vitaliipsl/crappy-memory/index/vector"
	"github.com/vitaliipsl/crappy-memory/index/vector/storage/inmemory"
	"github.com/vitaliipsl/crappy-memory/retrieval"
)

func TestNewReconcilesVectorsAndReusesCurrentEmbeddings(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	source := items(
		retrieval.Item{ID: "a", Scope: "personal", Text: "unchanged", Version: 1},
		retrieval.Item{ID: "b", Scope: "project", Text: "corrected", Version: 2},
		retrieval.Item{ID: "c", Scope: "personal", Text: "missing", Version: 1},
	)

	store := inmemory.New()
	if err := store.Upsert(ctx, []vector.Record{
		{ID: "a", Scope: "personal", Version: 1, Space: "test-space", Values: []float32{1, 0}},
		{ID: "b", Scope: "personal", Version: 1, Space: "test-space", Values: []float32{1, 0}},
		{ID: "orphan", Scope: "personal", Version: 5, Space: "test-space", Values: []float32{1, 1}},
	}); err != nil {
		t.Fatal(err)
	}

	documentCalls, queryCalls := 0, 0

	embedder := newEmbedder("test-space", func(_ context.Context, texts []string) ([][]float32, error) {
		documentCalls++

		if !slices.Equal(texts, []string{"corrected", "missing"}) {
			t.Fatalf("document embedding input = %v", texts)
		}

		return [][]float32{{0, 1}, {-1, 0}}, nil
	}, func(_ context.Context, text string) ([]float32, error) {
		queryCalls++

		if text != "semantic query" {
			t.Fatalf("query embedding input = %q", text)
		}

		return []float32{0, 1}, nil
	})
	if _, err := vector.New(ctx, source, store, embedder); err != nil {
		t.Fatal(err)
	}

	index, err := vector.New(ctx, source, store, embedder)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := store.Get(ctx, "orphan"); !errors.Is(err, vector.ErrNotFound) {
		t.Fatalf("orphaned vector was retained: %v", err)
	}

	candidates, err := index.Retrieve(ctx, retrieval.NewQuery("semantic query", retrieval.WithScopes("project"), retrieval.WithIDs("b"), retrieval.WithLimit(1)))
	if err != nil || len(candidates) != 1 || candidates[0].ID != "b" || candidates[0].Version != 2 || candidates[0].Score != 1 {
		t.Fatalf("reconciled candidates = %+v, %v", candidates, err)
	}

	if documentCalls != 1 || queryCalls != 1 {
		t.Fatalf("document calls=%d, query calls=%d; want one each", documentCalls, queryCalls)
	}
}

func TestNewRefreshesVectorsWhenEmbeddingSpaceChanges(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	source := items(retrieval.Item{ID: "note", Scope: "personal", Text: "saved note", Version: 3})

	store := inmemory.New()
	if err := store.Upsert(ctx, []vector.Record{{ID: "note", Scope: "personal", Version: 3, Space: "old-space", Values: []float32{1, 0}}}); err != nil {
		t.Fatal(err)
	}

	documentCalls := 0

	embedder := newEmbedder("new-space", func(_ context.Context, texts []string) ([][]float32, error) {
		documentCalls++

		if !slices.Equal(texts, []string{"saved note"}) {
			t.Fatalf("space refresh input = %v", texts)
		}

		return [][]float32{{0, 1, 0}}, nil
	}, func(context.Context, string) ([]float32, error) {
		return []float32{0, 1, 0}, nil
	})
	if _, err := vector.New(ctx, source, store, embedder); err != nil {
		t.Fatal(err)
	}

	index, err := vector.New(ctx, source, store, embedder)
	if err != nil {
		t.Fatal(err)
	}

	record, err := store.Get(ctx, "note")
	if err != nil || record.Space != "new-space" || record.Version != 3 || !slices.Equal(record.Values, []float32{0, 1, 0}) || documentCalls != 1 {
		t.Fatalf("space refresh = %+v, %v; document calls=%d", record, err, documentCalls)
	}

	candidates, err := index.Retrieve(ctx, retrieval.NewQuery("query"))
	if err != nil || len(candidates) != 1 || candidates[0].ID != "note" || candidates[0].Score != 1 {
		t.Fatalf("retrieval after space refresh = %+v, %v", candidates, err)
	}
}

func TestNewRequiresEmbeddingSpace(t *testing.T) {
	t.Parallel()

	source := func(func(retrieval.Item, error) bool) {
		t.Fatal("missing space caused item loading")
	}

	embedder := newEmbedder("", func(context.Context, []string) ([][]float32, error) {
		t.Fatal("missing space caused embedding")

		return nil, nil
	}, nil)
	if index, err := vector.New(t.Context(), source, inmemory.New(), embedder); index != nil || err == nil {
		t.Fatalf("missing space accepted: %v, %v", index, err)
	}
}

func TestNewEmbedsItemsInBatches(t *testing.T) {
	t.Parallel()

	var stored []retrieval.Item
	for position := range 101 {
		stored = append(stored, retrieval.Item{ID: fmt.Sprintf("%03d", position), Text: "note", Version: 1})
	}

	var batches []int

	embedder := newEmbedder("test-space", func(_ context.Context, texts []string) ([][]float32, error) {
		batches = append(batches, len(texts))

		embeddings := make([][]float32, len(texts))
		for position := range embeddings {
			embeddings[position] = []float32{1, 0}
		}

		return embeddings, nil
	}, nil)

	store := inmemory.New()
	if _, err := vector.New(t.Context(), items(stored...), store, embedder); err != nil {
		t.Fatal(err)
	}

	page, err := store.List(t.Context(), "", len(stored)+1)
	if err != nil || len(page.Records) != len(stored) || !slices.Equal(batches, []int{100, 1}) {
		t.Fatalf("initialization records=%d, batches=%v, error=%v", len(page.Records), batches, err)
	}
}

func TestIndexTracksUpsertsAndRemovals(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	embedder := newEmbedder("test-space", func(_ context.Context, texts []string) ([][]float32, error) {
		if len(texts) != 1 {
			t.Fatalf("runtime batch = %v", texts)
		}

		switch texts[0] {
		case "Prefer brief answers":
			return [][]float32{{1, 0}}, nil
		case "Prefer detailed answers":
			return [][]float32{{0, 1}}, nil
		default:
			t.Fatalf("unexpected document embedding input: %v", texts)

			return nil, nil
		}
	}, func(_ context.Context, text string) ([]float32, error) {
		switch text {
		case "concise responses":
			return []float32{1, 0}, nil
		case "long explanations":
			return []float32{0, 1}, nil
		default:
			t.Fatalf("unexpected query embedding input: %q", text)

			return nil, nil
		}
	})

	index, err := vector.New(ctx, items(), inmemory.New(), embedder)
	if err != nil {
		t.Fatal(err)
	}

	item := retrieval.Item{ID: "note", Scope: "personal", Text: "Prefer brief answers", Version: 1}
	if err := index.Upsert(ctx, item); err != nil {
		t.Fatal(err)
	}

	candidates, err := index.Retrieve(ctx, retrieval.NewQuery("concise responses"))
	if err != nil || len(candidates) != 1 || candidates[0].ID != item.ID || candidates[0].Score != 1 {
		t.Fatalf("semantic recall = %+v, %v", candidates, err)
	}

	item.Text = "Prefer detailed answers"
	item.Version++

	if err := index.Upsert(ctx, item); err != nil {
		t.Fatal(err)
	}

	candidates, err = index.Retrieve(ctx, retrieval.NewQuery("long explanations"))
	if err != nil || len(candidates) != 1 || candidates[0].Version != item.Version || candidates[0].Score != 1 {
		t.Fatalf("updated semantic recall = %+v, %v", candidates, err)
	}

	if err := index.Remove(ctx, item.ID); err != nil {
		t.Fatal(err)
	}

	candidates, err = index.Retrieve(ctx, retrieval.NewQuery("long explanations"))
	if err != nil || len(candidates) != 0 {
		t.Fatalf("removed semantic memory = %+v, %v", candidates, err)
	}
}

func TestIndexIgnoresOlderAndCurrentVersions(t *testing.T) {
	t.Parallel()

	ctx := t.Context()

	var embedded []string

	embedder := newEmbedder("test-space", func(_ context.Context, texts []string) ([][]float32, error) {
		embedded = append(embedded, texts...)

		return [][]float32{{1, 0}}, nil
	}, nil)

	store := inmemory.New()

	index, err := vector.New(ctx, items(), store, embedder)
	if err != nil {
		t.Fatal(err)
	}

	for _, item := range []retrieval.Item{
		{ID: "note", Scope: "personal", Text: "current", Version: 3},
		{ID: "note", Scope: "personal", Text: "outdated", Version: 2},
		{ID: "note", Scope: "personal", Text: "current", Version: 3},
	} {
		if err := index.Upsert(ctx, item); err != nil {
			t.Fatal(err)
		}
	}

	record, err := store.Get(ctx, "note")
	if err != nil || record.Version != 3 || !slices.Equal(embedded, []string{"current"}) {
		t.Fatalf("stored vector = %+v, %v; embedded %v", record, err, embedded)
	}
}

func TestIndexPropagatesEmbeddingFailures(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	source := items(retrieval.Item{ID: "note", Text: "note", Version: 1})
	store := inmemory.New()
	failure := errors.New("embedding unavailable")

	embedder := newEmbedder("test-space", func(context.Context, []string) ([][]float32, error) {
		return nil, failure
	}, nil)
	if index, err := vector.New(ctx, source, store, embedder); index != nil || !errors.Is(err, failure) {
		t.Fatalf("failed initialization = %v, %v", index, err)
	}

	embedder = newEmbedder("test-space", func(context.Context, []string) ([][]float32, error) { return nil, nil }, nil)
	if index, err := vector.New(ctx, source, store, embedder); index != nil || err == nil {
		t.Fatalf("misaligned embeddings were accepted: %v, %v", index, err)
	}

	if _, err := store.Get(ctx, "note"); !errors.Is(err, vector.ErrNotFound) {
		t.Fatalf("failed embedding wrote a vector: %v", err)
	}

	embedder = newEmbedder("test-space", func(context.Context, []string) ([][]float32, error) {
		return [][]float32{{0, 0}}, nil
	}, nil)
	if index, err := vector.New(ctx, source, store, embedder); index != nil || err == nil {
		t.Fatalf("zero vector was accepted: %v, %v", index, err)
	}

	embedder = newEmbedder("test-space", nil, func(context.Context, string) ([]float32, error) {
		return nil, failure
	})

	index, err := vector.New(ctx, items(), store, embedder)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := index.Retrieve(ctx, retrieval.NewQuery("query")); !errors.Is(err, failure) {
		t.Fatalf("query embedding failure = %v", err)
	}

	ctx, cancel := context.WithCancel(ctx)
	cancel()

	if index, err := vector.New(ctx, source, store, embedder); index != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled initialization = %v, %v", index, err)
	}
}

func TestNewPropagatesItemErrors(t *testing.T) {
	t.Parallel()

	failure := errors.New("item store unavailable")
	source := func(yield func(retrieval.Item, error) bool) {
		yield(retrieval.Item{}, failure)
	}

	embedder := newEmbedder("test-space", func(context.Context, []string) ([][]float32, error) {
		t.Fatal("failed item loading attempted embedding")

		return nil, nil
	}, nil)
	if index, err := vector.New(t.Context(), source, inmemory.New(), embedder); index != nil || !errors.Is(err, failure) {
		t.Fatalf("item loading failure = %v, %v", index, err)
	}
}

func TestNewRefreshesAnyVersionMismatch(t *testing.T) {
	t.Parallel()

	ctx := t.Context()

	store := inmemory.New()
	if err := store.Upsert(ctx, []vector.Record{{ID: "note", Version: 2, Space: "test-space", Values: []float32{1, 0}}}); err != nil {
		t.Fatal(err)
	}

	embedder := newEmbedder("test-space", func(_ context.Context, texts []string) ([][]float32, error) {
		if !slices.Equal(texts, []string{"canonical"}) {
			t.Fatalf("version mismatch embedded %v", texts)
		}

		return [][]float32{{0, 1}}, nil
	}, nil)
	if _, err := vector.New(ctx, items(retrieval.Item{ID: "note", Text: "canonical", Version: 1}), store, embedder); err != nil {
		t.Fatal(err)
	}

	record, err := store.Get(ctx, "note")
	if err != nil || record.Version != 1 || !slices.Equal(record.Values, []float32{0, 1}) {
		t.Fatalf("version mismatch was not reconciled: %+v, %v", record, err)
	}
}

func TestNewReusesCommittedBatchesAfterInterruptedInitialization(t *testing.T) {
	t.Parallel()

	ctx := t.Context()

	var stored []retrieval.Item
	for position := range 101 {
		id := fmt.Sprintf("item-%03d", position)
		stored = append(stored, retrieval.Item{ID: id, Text: id, Version: 1})
	}

	store := inmemory.New()
	calls := 0
	wantErr := errors.New("embedding unavailable")

	embedder := newEmbedder("space", func(_ context.Context, texts []string) ([][]float32, error) {
		calls++
		if calls == 2 {
			return nil, wantErr
		}

		embeddings := make([][]float32, len(texts))
		for position := range embeddings {
			embeddings[position] = []float32{1}
		}

		return embeddings, nil
	}, nil)
	if index, err := vector.New(ctx, items(stored...), store, embedder); index != nil || !errors.Is(err, wantErr) {
		t.Fatalf("interrupted initialization = %v, %v", index, err)
	}

	calls = 0

	embedder = newEmbedder("space", func(_ context.Context, texts []string) ([][]float32, error) {
		calls++

		if !slices.Equal(texts, []string{"item-100"}) {
			t.Fatalf("retried already committed embeddings: %v", texts)
		}

		return [][]float32{{1}}, nil
	}, nil)
	if _, err := vector.New(ctx, items(stored...), store, embedder); err != nil {
		t.Fatal(err)
	}

	if calls != 1 {
		t.Fatalf("remaining embedding batches = %d, want 1", calls)
	}
}

type testEmbedder struct {
	space     string
	documents func(context.Context, []string) ([][]float32, error)
	query     func(context.Context, string) ([]float32, error)
}

func newEmbedder(space string, documents func(context.Context, []string) ([][]float32, error), query func(context.Context, string) ([]float32, error)) *testEmbedder {
	return &testEmbedder{space: space, documents: documents, query: query}
}

func (e *testEmbedder) Space() string {
	return e.space
}

func (e *testEmbedder) EmbedDocuments(ctx context.Context, texts []string) ([][]float32, error) {
	return e.documents(ctx, texts)
}

func (e *testEmbedder) EmbedQuery(ctx context.Context, text string) ([]float32, error) {
	return e.query(ctx, text)
}

func items(values ...retrieval.Item) iter.Seq2[retrieval.Item, error] {
	return func(yield func(retrieval.Item, error) bool) {
		for _, item := range values {
			if !yield(item, nil) {
				return
			}
		}
	}
}
