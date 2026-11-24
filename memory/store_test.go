package memory_test

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/vitaliipsl/crappy-memory/memory"
	"github.com/vitaliipsl/crappy-memory/retrieval"
)

func TestItemsPagesThroughStore(t *testing.T) {
	t.Parallel()

	entries := []memory.Entry{
		{ID: "a", Scope: "personal", Content: "first", Source: "conversation:1", Version: 3},
		{ID: "b", Scope: "project", Content: "second", Version: 1},
		{ID: "c", Scope: "personal", Content: "third", Version: 2},
	}
	pages := []memory.Page{{Entries: entries[:2], NextID: "b"}, {Entries: entries[2:]}}

	var reads []memory.Filter

	store := newListStore(func(_ context.Context, filter memory.Filter) (memory.Page, error) {
		reads = append(reads, filter)

		return pages[len(reads)-1], nil
	})

	var items []retrieval.Item

	for item, err := range memory.Items(t.Context(), store) {
		if err != nil {
			t.Fatal(err)
		}

		items = append(items, item)
	}

	want := []retrieval.Item{
		{ID: "a", Scope: "personal", Version: 3, Text: "first"},
		{ID: "b", Scope: "project", Version: 1, Text: "second"},
		{ID: "c", Scope: "personal", Version: 2, Text: "third"},
	}
	if !slices.Equal(items, want) {
		t.Fatalf("items = %+v, want %+v", items, want)
	}

	if !slices.EqualFunc(reads, []memory.Filter{{Limit: memory.DefaultPageSize}, {AfterID: "b", Limit: memory.DefaultPageSize}}, func(got, want memory.Filter) bool {
		return got.AfterID == want.AfterID && got.Limit == want.Limit && len(got.Scopes) == 0 && len(got.IDs) == 0
	}) {
		t.Fatalf("store reads = %+v", reads)
	}
}

func TestItemsEndsAtStoreFailure(t *testing.T) {
	t.Parallel()

	failure := errors.New("store unavailable")
	store := newListStore(func(_ context.Context, filter memory.Filter) (memory.Page, error) {
		if filter.AfterID == "" {
			return memory.Page{Entries: []memory.Entry{{ID: "a", Version: 1}}, NextID: "a"}, nil
		}

		return memory.Page{}, failure
	})

	var (
		ids  []string
		errs []error
	)

	for item, err := range memory.Items(t.Context(), store) {
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
	store := newListStore(func(context.Context, memory.Filter) (memory.Page, error) {
		reads++

		return memory.Page{Entries: []memory.Entry{{ID: "a", Version: 1}, {ID: "b", Version: 1}}, NextID: "b"}, nil
	})

	for range memory.Items(t.Context(), store) {
		break
	}

	if reads != 1 {
		t.Fatalf("store reads after an early stop = %d, want 1", reads)
	}
}

type listStore struct {
	memory.Store
	list func(context.Context, memory.Filter) (memory.Page, error)
}

func newListStore(list func(context.Context, memory.Filter) (memory.Page, error)) *listStore {
	return &listStore{list: list}
}

func (s *listStore) List(ctx context.Context, filter memory.Filter) (memory.Page, error) {
	return s.list(ctx, filter)
}
