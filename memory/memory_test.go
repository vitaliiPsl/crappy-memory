package memory_test

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/vitaliipsl/crappy-memory/memory"
	"github.com/vitaliipsl/crappy-memory/retrieval"
	jsonstore "github.com/vitaliipsl/crappy-memory/storage/json"
)

func TestMemoryEntryLifecycle(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	store := openJSONStore(t, filepath.Join(t.TempDir(), "entries.json"))
	mem := memory.New(store)

	created, err := mem.Create(ctx, "personal", "Keep useful comments.\n",
		memory.WithSource("session:original/message:1"),
		memory.WithMetadata("category", "preference"),
		memory.WithMetadata("obsolete", true),
	)
	if err != nil {
		t.Fatal(err)
	}

	if created.ID == "" || created.Version != 1 || created.Scope != "personal" || created.Content != "Keep useful comments.\n" {
		t.Fatalf("created entry = %+v", created)
	}

	if created.CreatedAt.IsZero() || created.CreatedAt.Location() != time.UTC || created.UpdatedAt != created.CreatedAt {
		t.Fatalf("entry timestamps = %+v", created)
	}

	updated, err := mem.Update(ctx, created.ID, created.Version, "Explain intent.",
		memory.WithSource("session:correction/message:2"),
		memory.WithMetadata("category", "convention"),
		memory.WithoutMetadata("obsolete"),
	)
	if err != nil {
		t.Fatal(err)
	}

	if updated.ID != created.ID || updated.Scope != created.Scope || updated.CreatedAt != created.CreatedAt || updated.Version != 2 {
		t.Fatalf("updated identity or version = %+v", updated)
	}

	if updated.Source != "session:correction/message:2" || string(updated.Metadata["category"]) != `"convention"` || len(updated.Metadata) != 1 {
		t.Fatalf("updated source and metadata = %+v", updated)
	}

	if updated.Content != "Explain intent." || updated.UpdatedAt.Before(created.UpdatedAt) {
		t.Fatalf("updated entry = %+v", updated)
	}

	loaded, err := mem.Get(ctx, created.ID)
	if err != nil {
		t.Fatal(err)
	}

	assertEntry(t, loaded, updated)

	if err := mem.Delete(ctx, updated.ID, updated.Version); err != nil {
		t.Fatal(err)
	}

	if _, err := mem.Get(ctx, updated.ID); !errors.Is(err, memory.ErrNotFound) {
		t.Fatalf("Get(deleted) error = %v, want ErrNotFound", err)
	}

	if err := mem.Delete(ctx, updated.ID, updated.Version); !errors.Is(err, memory.ErrNotFound) {
		t.Fatalf("Delete(deleted) error = %v, want ErrNotFound", err)
	}
}

func TestMemoryListsEntriesInCreationOrder(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	mem := memory.New(openJSONStore(t, filepath.Join(t.TempDir(), "entries.json")))

	created := make([]string, 0, 10)
	for range 10 {
		entry, err := mem.Create(ctx, "personal", "remembered")
		if err != nil {
			t.Fatal(err)
		}

		created = append(created, entry.ID)
	}

	page, err := mem.List(ctx, memory.Filter{})
	if err != nil {
		t.Fatal(err)
	}

	listed := make([]string, 0, len(page.Entries))
	for _, entry := range page.Entries {
		listed = append(listed, entry.ID)
	}

	if !slices.Equal(listed, created) {
		t.Fatalf("listed IDs = %v, want creation order %v", listed, created)
	}
}

func TestMemoryRejectsStaleUpdatesAndDeletes(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	store := openJSONStore(t, filepath.Join(t.TempDir(), "entries.json"))
	first := memory.New(store)
	second := memory.New(store)

	created, err := first.Create(ctx, "personal", "original")
	if err != nil {
		t.Fatal(err)
	}

	updated, err := second.Update(ctx, created.ID, created.Version, "newer")
	if err != nil {
		t.Fatal(err)
	}

	if _, err := first.Update(ctx, created.ID, created.Version, "stale"); !errors.Is(err, memory.ErrConflict) {
		t.Fatalf("Update(stale) error = %v, want ErrConflict", err)
	}

	if err := first.Delete(ctx, created.ID, created.Version); !errors.Is(err, memory.ErrConflict) {
		t.Fatalf("Delete(stale) error = %v, want ErrConflict", err)
	}

	loaded, err := first.Get(ctx, created.ID)
	if err != nil {
		t.Fatal(err)
	}

	assertEntry(t, loaded, updated)
}

func TestMemoryScopesAreFlatNamespaces(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	store := openJSONStore(t, filepath.Join(t.TempDir(), "entries.json"))
	writer := memory.New(store)

	entries := make(map[string]memory.Entry)
	for _, scope := range []string{"personal", "project:crappy", "project:crappy/child"} {
		entry, err := writer.Create(ctx, scope, "knowledge")
		if err != nil {
			t.Fatal(err)
		}

		entries[scope] = entry
	}

	reader := memory.New(store)

	page, err := reader.List(ctx, memory.Filter{})
	if err != nil || len(page.Entries) != 3 {
		t.Fatalf("List() = %+v, error = %v, want all entries", page, err)
	}

	page, err = reader.List(ctx, memory.Filter{Scopes: []string{"project:crappy"}})
	if err != nil || len(page.Entries) != 1 {
		t.Fatalf("List(project:crappy) = %+v, error = %v, want only the exact scope", page, err)
	}

	assertEntry(t, page.Entries[0], entries["project:crappy"])

	loaded, err := reader.Get(ctx, entries["personal"].ID)
	if err != nil {
		t.Fatal(err)
	}

	assertEntry(t, loaded, entries["personal"])
}

func TestMemoryMetadataOptionsPreserveUnchangedFields(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	mem := memory.New(openJSONStore(t, filepath.Join(t.TempDir(), "entries.json")))

	created, err := mem.Create(ctx, "personal", "original",
		memory.WithSource("opaque:reference"),
		memory.WithMetadata("settings", map[string]any{"count": 9007199254740993, "tags": []string{"one", "two"}}),
		memory.WithMetadata("nullable", nil),
	)
	if err != nil {
		t.Fatal(err)
	}

	updated, err := mem.Update(ctx, created.ID, created.Version, "corrected")
	if err != nil {
		t.Fatal(err)
	}

	if updated.Source != created.Source || string(updated.Metadata["settings"]) != string(created.Metadata["settings"]) || string(updated.Metadata["nullable"]) != "null" {
		t.Fatalf("update changed untouched source or metadata: %+v", updated)
	}

	if _, err := mem.Update(ctx, updated.ID, updated.Version, "invalid",
		memory.WithSource("unsaved"),
		memory.WithMetadata("settings", "unsaved"),
		memory.WithoutMetadata("nullable"),
		memory.WithMetadata("unsupported", make(chan int)),
	); err == nil {
		t.Fatal("Update() accepted non-JSON metadata")
	}

	loaded, err := mem.Get(ctx, updated.ID)
	if err != nil {
		t.Fatal(err)
	}

	assertEntry(t, loaded, updated)

	cleared, err := mem.Update(ctx, updated.ID, updated.Version, "corrected",
		memory.WithSource(""), memory.WithoutMetadata("settings"), memory.WithoutMetadata("nullable"),
	)
	if err != nil {
		t.Fatal(err)
	}

	if cleared.Source != "" || len(cleared.Metadata) != 0 {
		t.Fatalf("cleared entry = %+v", cleared)
	}

	if _, err := mem.Create(ctx, "personal", "invalid", memory.WithMetadata("unsupported", make(chan int))); err == nil {
		t.Fatal("Create() accepted non-JSON metadata")
	}

	loaded, err = mem.Get(ctx, cleared.ID)
	if err != nil {
		t.Fatal(err)
	}

	assertEntry(t, loaded, cleared)
}

func TestMemoryReturnsBackendErrors(t *testing.T) {
	t.Parallel()

	store := openJSONStore(t, filepath.Join(t.TempDir(), "entries.json"))
	owner := memory.New(store)

	created, err := owner.Create(t.Context(), "personal", "original")
	if err != nil {
		t.Fatal(err)
	}

	failure := errors.New("storage unavailable")

	mem := memory.New(newFailingStore(store, failure))
	if entry, err := mem.Create(t.Context(), "personal", "new"); !errors.Is(err, failure) || entry.ID != "" {
		t.Fatalf("Create() = %+v, %v, want storage error without entry", entry, err)
	}

	if entry, err := mem.Update(t.Context(), created.ID, created.Version, "replacement"); !errors.Is(err, failure) || entry.ID != "" {
		t.Fatalf("Update() = %+v, %v, want storage error without entry", entry, err)
	}

	loaded, err := owner.Get(t.Context(), created.ID)
	if err != nil {
		t.Fatal(err)
	}

	assertEntry(t, loaded, created)
}

type failingStore struct {
	memory.Store
	err error
}

func newFailingStore(store memory.Store, err error) *failingStore {
	return &failingStore{Store: store, err: err}
}

func (s *failingStore) Create(context.Context, memory.Entry) error {
	return s.err
}

func (s *failingStore) Update(context.Context, memory.Entry, uint64) error {
	return s.err
}

func (s *failingStore) Delete(context.Context, string, uint64) error {
	return s.err
}

func TestMemoryRejectsEmptyContent(t *testing.T) {
	t.Parallel()

	mem := memory.New(openJSONStore(t, filepath.Join(t.TempDir(), "entries.json")))
	if _, err := mem.Create(t.Context(), "personal", " \n "); err == nil {
		t.Fatal("Create() accepted empty content")
	}

	created, err := mem.Create(t.Context(), "personal", "original")
	if err != nil {
		t.Fatal(err)
	}

	if _, err := mem.Update(t.Context(), created.ID, created.Version, ""); err == nil {
		t.Fatal("Update() accepted empty content")
	}

	if _, err := mem.Update(t.Context(), created.ID, 0, "replacement"); !errors.Is(err, memory.ErrConflict) {
		t.Fatalf("Update(no version) error = %v, want ErrConflict", err)
	}
}

func TestMemorySearchRequiresRetriever(t *testing.T) {
	t.Parallel()

	store := openJSONStore(t, filepath.Join(t.TempDir(), "entries.json"))
	if results, err := memory.New(store).Search(t.Context(), "query"); !errors.Is(err, memory.ErrRetrieverRequired) || results != nil {
		t.Fatalf("unconfigured search = %+v, %v", results, err)
	}
}

func TestMemorySearchResolvesCandidatesWithOneStoreRead(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	first := memory.Entry{ID: "first", Scope: "selected", Content: "first", Version: 1}
	second := memory.Entry{ID: "second", Scope: "selected", Content: "second", Version: 3}
	wrongScope := memory.Entry{ID: "wrong-scope", Scope: "selected/child", Content: "child", Version: 1}
	excluded := memory.Entry{ID: "excluded", Scope: "selected", Content: "excluded", Version: 1}
	stale := memory.Entry{ID: "stale", Scope: "selected", Content: "newer", Version: 2}
	entries := []memory.Entry{first, second, wrongScope, excluded, stale}

	var reads []memory.Filter

	store := newListStore(func(_ context.Context, filter memory.Filter) (memory.Page, error) {
		reads = append(reads, filter)

		var page memory.Page
		for _, entry := range entries {
			if slices.Contains(filter.IDs, entry.ID) {
				page.Entries = append(page.Entries, entry)
			}
		}

		return page, nil
	})

	candidates := []retrieval.Candidate{
		{ID: "deleted", Version: 1, Score: 100},
		{ID: wrongScope.ID, Version: 1, Score: 90},
		{ID: stale.ID, Version: 1, Score: 80},
		{ID: excluded.ID, Version: 1, Score: 70},
		{ID: first.ID, Version: 2, Score: 60},
		{ID: first.ID, Version: 1, Score: 5},
		{ID: first.ID, Version: 1, Score: 4},
		{ID: second.ID, Score: 2},
	}
	ids := []string{first.ID, second.ID, wrongScope.ID, stale.ID, "deleted"}
	retriever := retrieverFunc(func(received context.Context, query retrieval.Query) ([]retrieval.Candidate, error) {
		if received != ctx || query.Text != "question" || query.Limit != 2 || !slices.Equal(query.Scopes, []string{"selected"}) || !slices.Equal(query.IDs, ids) {
			t.Fatalf("retriever query = %+v", query)
		}

		return candidates, nil
	})

	results, err := memory.New(store, memory.WithRetriever(retriever)).Search(ctx, "question",
		retrieval.WithScopes("selected"), retrieval.WithIDs(ids...), retrieval.WithLimit(2),
	)
	if err != nil || !reflect.DeepEqual(results, []memory.SearchResult{{Entry: first, Score: 5}, {Entry: second, Score: 2}}) {
		t.Fatalf("resolved results = %+v, %v", results, err)
	}

	candidateIDs := []string{"deleted", wrongScope.ID, stale.ID, excluded.ID, first.ID, first.ID, first.ID, second.ID}
	if len(reads) != 1 || !slices.Equal(reads[0].IDs, candidateIDs) || reads[0].Limit != len(candidateIDs) {
		t.Fatalf("store reads = %+v", reads)
	}
}

func TestMemorySearchWithoutCandidatesSkipsStore(t *testing.T) {
	t.Parallel()

	store := newListStore(func(context.Context, memory.Filter) (memory.Page, error) {
		t.Fatal("search without candidates read the store")

		return memory.Page{}, nil
	})
	retriever := retrieverFunc(func(context.Context, retrieval.Query) ([]retrieval.Candidate, error) {
		return nil, nil
	})

	results, err := memory.New(store, memory.WithRetriever(retriever)).Search(t.Context(), "question")
	if err != nil || results == nil || len(results) != 0 {
		t.Fatalf("empty search = %+v, %v", results, err)
	}
}

func TestMemorySearchPropagatesFailuresWithoutPartialResults(t *testing.T) {
	t.Parallel()

	failure := errors.New("retrieval unavailable")
	store := newListStore(func(context.Context, memory.Filter) (memory.Page, error) {
		return memory.Page{Entries: []memory.Entry{{ID: "note", Version: 1}}}, nil
	})
	retriever := retrieverFunc(func(context.Context, retrieval.Query) ([]retrieval.Candidate, error) {
		return []retrieval.Candidate{{ID: "note", Version: 1}}, failure
	})

	if results, err := memory.New(store, memory.WithRetriever(retriever)).Search(t.Context(), "question"); !errors.Is(err, failure) || results != nil {
		t.Fatalf("failed retrieval = %+v, %v", results, err)
	}

	failure = errors.New("store unavailable")
	store = newListStore(func(context.Context, memory.Filter) (memory.Page, error) {
		return memory.Page{}, failure
	})
	retriever = retrieverFunc(func(context.Context, retrieval.Query) ([]retrieval.Candidate, error) {
		return []retrieval.Candidate{{ID: "note", Version: 1}}, nil
	})

	if results, err := memory.New(store, memory.WithRetriever(retriever)).Search(t.Context(), "question"); !errors.Is(err, failure) || results != nil {
		t.Fatalf("failed store read = %+v, %v", results, err)
	}
}

type retrieverFunc func(context.Context, retrieval.Query) ([]retrieval.Candidate, error)

func (fn retrieverFunc) Retrieve(ctx context.Context, query retrieval.Query) ([]retrieval.Candidate, error) {
	return fn(ctx, query)
}

func TestMemoryReportsIndexFailuresWithoutFailingCanonicalWrites(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	store := openJSONStore(t, filepath.Join(t.TempDir(), "entries.json"))
	failure := errors.New("index unavailable")

	var failures []error

	broken := newTestIndexer(
		func(ctx context.Context, item retrieval.Item) error {
			loaded, err := store.Get(ctx, item.ID)
			if err != nil || loaded.Version != item.Version || loaded.Scope != item.Scope || loaded.Content != item.Text {
				t.Fatalf("indexing ran before persistence: %+v, %v", loaded, err)
			}

			return failure
		},
		func(ctx context.Context, id string) error {
			if _, err := store.Get(ctx, id); !errors.Is(err, memory.ErrNotFound) {
				t.Fatalf("index removal ran before deletion: %v", err)
			}

			return failure
		},
	)
	indexed := make(map[string]uint64)
	healthy := newTestIndexer(
		func(_ context.Context, item retrieval.Item) error {
			indexed[item.ID] = item.Version

			return nil
		},
		func(_ context.Context, id string) error {
			delete(indexed, id)

			return nil
		},
	)
	mem := memory.New(store,
		memory.WithIndexer(broken, func(_ context.Context, err error) { failures = append(failures, err) }),
		memory.WithIndexer(healthy, func(_ context.Context, err error) { t.Error(err) }),
	)

	created, err := mem.Create(ctx, "personal", "original")
	if err != nil {
		t.Fatalf("index failure changed create result: %v", err)
	}

	updated, err := mem.Update(ctx, created.ID, created.Version, "replacement")
	if err != nil {
		t.Fatalf("index failure changed update result: %v", err)
	}

	loaded, err := mem.Get(ctx, updated.ID)
	if err != nil {
		t.Fatal(err)
	}

	assertEntry(t, loaded, updated)

	if indexed[updated.ID] != updated.Version {
		t.Fatalf("a failing index prevented another from updating: %v", indexed)
	}

	if err := mem.Delete(ctx, updated.ID, updated.Version); err != nil {
		t.Fatalf("index failure changed delete result: %v", err)
	}

	if len(indexed) != 0 {
		t.Fatalf("a failing index prevented another from removing: %v", indexed)
	}

	if len(failures) != 3 {
		t.Fatalf("index failures = %v, want one for each committed mutation", failures)
	}

	for _, err := range failures {
		if !errors.Is(err, failure) {
			t.Fatalf("index failure lost its cause: %v", err)
		}
	}
}

func TestMemoryDoesNotIndexFailedCanonicalWrites(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	store := openJSONStore(t, filepath.Join(t.TempDir(), "entries.json"))
	owner := memory.New(store)

	created, err := owner.Create(ctx, "personal", "original")
	if err != nil {
		t.Fatal(err)
	}

	notifications := 0
	index := newTestIndexer(
		func(context.Context, retrieval.Item) error {
			notifications++

			return nil
		},
		func(context.Context, string) error {
			notifications++

			return nil
		},
	)
	option := memory.WithIndexer(index, func(_ context.Context, err error) { t.Error(err) })
	failure := errors.New("store unavailable")
	broken := memory.New(newFailingStore(store, failure), option)

	if _, err := broken.Create(ctx, "personal", "new"); !errors.Is(err, failure) {
		t.Fatalf("create error = %v", err)
	}

	if _, err := broken.Update(ctx, created.ID, created.Version, "changed"); !errors.Is(err, failure) {
		t.Fatalf("update error = %v", err)
	}

	if err := broken.Delete(ctx, created.ID, created.Version); !errors.Is(err, failure) {
		t.Fatalf("delete error = %v", err)
	}

	mem := memory.New(store, option)
	if _, err := mem.Update(ctx, created.ID, 0, "stale"); !errors.Is(err, memory.ErrConflict) {
		t.Fatalf("stale update error = %v", err)
	}

	if err := mem.Delete(ctx, created.ID, 0); !errors.Is(err, memory.ErrConflict) {
		t.Fatalf("stale delete error = %v", err)
	}

	if notifications != 0 {
		t.Fatalf("failed writes triggered %d index notifications", notifications)
	}
}

func TestMemoryRepeatedDeleteRetriesIndexRemoval(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	store := openJSONStore(t, filepath.Join(t.TempDir(), "entries.json"))
	failure := errors.New("temporary removal failure")
	attempts := 0
	retrying := newTestIndexer(func(context.Context, retrieval.Item) error { return nil }, func(context.Context, string) error {
		attempts++
		if attempts == 1 {
			return failure
		}

		return nil
	})
	mem := memory.New(store,
		memory.WithIndexer(retrying, func(_ context.Context, err error) {
			if !errors.Is(err, failure) {
				t.Error(err)
			}
		}),
	)

	created, err := mem.Create(ctx, "personal", "remembered")
	if err != nil {
		t.Fatal(err)
	}

	if err := mem.Delete(ctx, created.ID, created.Version); err != nil {
		t.Fatal(err)
	}

	if err := mem.Delete(ctx, created.ID, created.Version); !errors.Is(err, memory.ErrNotFound) {
		t.Fatalf("Delete(deleted) error = %v, want ErrNotFound", err)
	}

	if attempts != 2 {
		t.Fatalf("index removal attempts = %d, want 2", attempts)
	}
}

type testIndexer struct {
	upsert func(context.Context, retrieval.Item) error
	remove func(context.Context, string) error
}

func newTestIndexer(upsert func(context.Context, retrieval.Item) error, remove func(context.Context, string) error) *testIndexer {
	return &testIndexer{upsert: upsert, remove: remove}
}

func (i *testIndexer) Upsert(ctx context.Context, item retrieval.Item) error {
	return i.upsert(ctx, item)
}

func (i *testIndexer) Remove(ctx context.Context, id string) error {
	return i.remove(ctx, id)
}

func assertEntry(t *testing.T, got, want memory.Entry) {
	t.Helper()

	gotJSON, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}

	wantJSON, err := json.Marshal(want)
	if err != nil {
		t.Fatal(err)
	}

	if string(gotJSON) != string(wantJSON) {
		t.Fatalf("entry = %s, want %s", gotJSON, wantJSON)
	}
}

func openJSONStore(t *testing.T, path string) memory.Store {
	t.Helper()

	store, err := jsonstore.New(path)
	if err != nil {
		t.Fatal(err)
	}

	return store
}
