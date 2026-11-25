package sqlite_test

import (
	"database/sql"
	"errors"
	"net/url"
	"path/filepath"
	"reflect"
	"slices"
	"testing"
	"time"

	_ "modernc.org/sqlite" // Register the SQLite test driver.

	"github.com/vitaliipsl/crappy-memory/source"
	"github.com/vitaliipsl/crappy-memory/source/storage/sqlite"
)

func TestStorePersistsDocumentsWithTheirChunks(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	path := filepath.Join(t.TempDir(), "sources.db")
	store := newStore(t, path)

	createdAt := time.Date(2026, 1, 1, 12, 30, 1, 123456789, time.UTC)
	document := source.Document{
		ID:        "notes/setup.md",
		Scope:     "project",
		MediaType: "text/markdown",
		Content:   []byte("# Setup\n多言語"),
		Hash:      "hash",
		Chunker:   "markdown/v1",
		CreatedAt: createdAt,
		UpdatedAt: createdAt.Add(time.Hour),
	}
	chunks := []source.Chunk{
		{ID: "b", DocumentID: document.ID, Scope: document.Scope, Position: 0, Location: "Setup, line 2", Content: "多言語"},
		{ID: "a", DocumentID: document.ID, Scope: document.Scope, Position: 1, Location: "line 3", Content: "second"},
	}

	if err := store.Put(ctx, document, chunks); err != nil {
		t.Fatal(err)
	}

	reopened := newStore(t, path)

	got, err := reopened.Get(ctx, document.ID)
	if err != nil || !reflect.DeepEqual(got, document) {
		t.Fatalf("Get() = %+v, %v, want %+v", got, err, document)
	}

	page, err := reopened.Chunks(ctx, source.ChunkFilter{DocumentIDs: []string{document.ID}})
	if err != nil || !slices.Equal(page.Chunks, []source.Chunk{chunks[1], chunks[0]}) {
		t.Fatalf("Chunks() = %+v, %v, want chunks in ID order", page.Chunks, err)
	}
}

func TestStorePutReplacesTheWholeChunkSet(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	store := newStore(t, filepath.Join(t.TempDir(), "sources.db"))

	document := source.Document{ID: "doc", Scope: "project", MediaType: "text/plain", Content: []byte("old")}
	if err := store.Put(ctx, document, []source.Chunk{
		{ID: "kept", DocumentID: "doc", Scope: "project", Position: 0, Content: "kept"},
		{ID: "dropped", DocumentID: "doc", Scope: "project", Position: 1, Content: "dropped"},
	}); err != nil {
		t.Fatal(err)
	}

	document.Content = []byte("new")

	replacement := []source.Chunk{
		{ID: "added", DocumentID: "doc", Scope: "project", Position: 0, Content: "added"},
		{ID: "kept", DocumentID: "doc", Scope: "project", Position: 1, Location: "moved", Content: "kept"},
	}
	if err := store.Put(ctx, document, replacement); err != nil {
		t.Fatal(err)
	}

	page, err := store.Chunks(ctx, source.ChunkFilter{})
	if err != nil || !slices.Equal(page.Chunks, replacement) {
		t.Fatalf("Chunks() = %+v, %v, want %+v", page.Chunks, err, replacement)
	}

	got, err := store.Get(ctx, "doc")
	if err != nil || string(got.Content) != "new" {
		t.Fatalf("Get() = %+v, %v, want replaced content", got, err)
	}
}

func TestStorePutKeepsThePreviousVersionWhenAChunkFails(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	store := newStore(t, filepath.Join(t.TempDir(), "sources.db"))

	original := source.Document{ID: "doc", Scope: "project", MediaType: "text/plain", Content: []byte("old")}
	if err := store.Put(ctx, original, []source.Chunk{{ID: "a", DocumentID: "doc", Scope: "project", Content: "old"}}); err != nil {
		t.Fatal(err)
	}

	duplicate := source.Chunk{ID: "same", DocumentID: "doc", Scope: "project", Content: "new"}
	if err := store.Put(ctx, source.Document{ID: "doc", Scope: "project", MediaType: "text/plain", Content: []byte("new")}, []source.Chunk{duplicate, duplicate}); err == nil {
		t.Fatal("Put(duplicate chunk IDs) error = nil")
	}

	got, err := store.Get(ctx, "doc")
	if err != nil || string(got.Content) != "old" {
		t.Fatalf("Get() after a failed put = %+v, %v, want the previous document", got, err)
	}

	page, err := store.Chunks(ctx, source.ChunkFilter{})
	if err != nil || len(page.Chunks) != 1 || page.Chunks[0].ID != "a" {
		t.Fatalf("Chunks() after a failed put = %+v, %v, want the previous chunks", page.Chunks, err)
	}
}

func TestStoreStoresEmptyDocuments(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	store := newStore(t, filepath.Join(t.TempDir(), "sources.db"))

	if err := store.Put(ctx, source.Document{ID: "empty", Scope: "project", MediaType: "text/plain"}, nil); err != nil {
		t.Fatal(err)
	}

	got, err := store.Get(ctx, "empty")
	if err != nil || len(got.Content) != 0 {
		t.Fatalf("Get(empty) = %+v, %v", got, err)
	}
}

func TestStoreDeletesDocumentsWithTheirChunks(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	store := newStore(t, filepath.Join(t.TempDir(), "sources.db"))

	for _, id := range []string{"deleted", "kept"} {
		if err := store.Put(ctx, source.Document{ID: id, Scope: "project", MediaType: "text/plain"}, []source.Chunk{
			{ID: id + "-chunk", DocumentID: id, Scope: "project", Content: id},
		}); err != nil {
			t.Fatal(err)
		}
	}

	if err := store.Delete(ctx, "deleted"); err != nil {
		t.Fatal(err)
	}

	if _, err := store.Get(ctx, "deleted"); !errors.Is(err, source.ErrNotFound) {
		t.Fatalf("Get(deleted) error = %v, want ErrNotFound", err)
	}

	page, err := store.Chunks(ctx, source.ChunkFilter{})
	if err != nil || len(page.Chunks) != 1 || page.Chunks[0].ID != "kept-chunk" {
		t.Fatalf("Chunks() after delete = %+v, %v", page.Chunks, err)
	}

	if err := store.Delete(ctx, "deleted"); !errors.Is(err, source.ErrNotFound) {
		t.Fatalf("Delete(deleted) error = %v, want ErrNotFound", err)
	}
}

func TestStoreListsFilteredDocumentPages(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	store := newStore(t, filepath.Join(t.TempDir(), "sources.db"))

	for _, document := range []source.Document{
		{ID: "c", Scope: "personal"},
		{ID: "a", Scope: "project"},
		{ID: "b", Scope: "personal"},
		{ID: "x' OR 1=1 --", Scope: "scope' ?"},
	} {
		if err := store.Put(ctx, document, nil); err != nil {
			t.Fatal(err)
		}
	}

	for _, test := range []struct {
		filter source.Filter
		want   []string
		nextID string
	}{
		{filter: source.Filter{Limit: 2}, want: []string{"a", "b"}, nextID: "b"},
		{filter: source.Filter{AfterID: "b"}, want: []string{"c", "x' OR 1=1 --"}},
		{filter: source.Filter{Scopes: []string{"personal"}}, want: []string{"b", "c"}},
		{filter: source.Filter{IDs: []string{"c", "a", "missing"}}, want: []string{"a", "c"}},
		{filter: source.Filter{Scopes: []string{"scope' ?"}}, want: []string{"x' OR 1=1 --"}},
		{filter: source.Filter{Limit: 4}, want: []string{"a", "b", "c", "x' OR 1=1 --"}},
	} {
		page, err := store.List(ctx, test.filter)
		if err != nil {
			t.Fatal(err)
		}

		got := make([]string, 0, len(page.Documents))
		for _, document := range page.Documents {
			got = append(got, document.ID)
		}

		if !slices.Equal(got, test.want) || page.NextID != test.nextID {
			t.Fatalf("List(%+v) = %v next %q, want %v next %q", test.filter, got, page.NextID, test.want, test.nextID)
		}
	}
}

func TestStoreListsFilteredChunkPages(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	store := newStore(t, filepath.Join(t.TempDir(), "sources.db"))

	for id, chunkIDs := range map[string][]string{"first": {"a", "c"}, "second": {"b", "d"}} {
		chunks := make([]source.Chunk, len(chunkIDs))
		for position, chunkID := range chunkIDs {
			chunks[position] = source.Chunk{ID: chunkID, DocumentID: id, Scope: "project", Position: position}
		}

		if err := store.Put(ctx, source.Document{ID: id, Scope: "project"}, chunks); err != nil {
			t.Fatal(err)
		}
	}

	for _, test := range []struct {
		filter source.ChunkFilter
		want   []string
		nextID string
	}{
		{filter: source.ChunkFilter{Limit: 3}, want: []string{"a", "b", "c"}, nextID: "c"},
		{filter: source.ChunkFilter{AfterID: "c"}, want: []string{"d"}},
		{filter: source.ChunkFilter{DocumentIDs: []string{"second"}}, want: []string{"b", "d"}},
		{filter: source.ChunkFilter{IDs: []string{"d", "a", "missing"}}, want: []string{"a", "d"}},
		{filter: source.ChunkFilter{DocumentIDs: []string{"first"}, IDs: []string{"b", "c"}}, want: []string{"c"}},
	} {
		page, err := store.Chunks(ctx, test.filter)
		if err != nil {
			t.Fatal(err)
		}

		got := make([]string, 0, len(page.Chunks))
		for _, chunk := range page.Chunks {
			got = append(got, chunk.ID)
		}

		if !slices.Equal(got, test.want) || page.NextID != test.nextID {
			t.Fatalf("Chunks(%+v) = %v next %q, want %v next %q", test.filter, got, page.NextID, test.want, test.nextID)
		}
	}
}

func TestStoreRejectsNegativeLimits(t *testing.T) {
	t.Parallel()

	store := newStore(t, filepath.Join(t.TempDir(), "sources.db"))

	if _, err := store.List(t.Context(), source.Filter{Limit: -1}); err == nil {
		t.Fatal("List(negative limit) error = nil")
	}

	if _, err := store.Chunks(t.Context(), source.ChunkFilter{Limit: -1}); err == nil {
		t.Fatal("Chunks(negative limit) error = nil")
	}
}

func newStore(t *testing.T, path string) *sqlite.Store {
	t.Helper()

	store, err := sqlite.New(openDatabase(t, path))
	if err != nil {
		t.Fatal(err)
	}

	return store
}

func openDatabase(t *testing.T, path string) *sql.DB {
	t.Helper()

	dsn := url.URL{Scheme: "file", Path: filepath.ToSlash(path)}
	parameters := url.Values{}
	parameters.Add("_pragma", "busy_timeout(5000)")
	parameters.Add("_pragma", "journal_mode(WAL)")
	dsn.RawQuery = parameters.Encode()

	db, err := sql.Open("sqlite", dsn.String())
	if err != nil {
		t.Fatal(err)
	}

	db.SetMaxOpenConns(1)
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Error(err)
		}
	})

	return db
}
