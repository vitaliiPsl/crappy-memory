package sqlite_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"math"
	"net/url"
	"path/filepath"
	"reflect"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	_ "modernc.org/sqlite" // Register the SQLite test driver.

	"github.com/vitaliipsl/crappy-memory/memory"
	"github.com/vitaliipsl/crappy-memory/storage/sqlite"
)

func TestStorePersistsEntries(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	path := filepath.Join(t.TempDir(), "entries.db")

	store, err := sqlite.New(openDatabase(t, path))
	if err != nil {
		t.Fatal(err)
	}

	createdAt := time.Date(2026, 1, 1, 12, 30, 1, 123456789, time.UTC)

	want := memory.Entry{
		ID:        "entry/with-an-opaque-id",
		Scope:     "personal",
		Content:   "original\n多言語",
		Source:    "session:1",
		Metadata:  map[string]json.RawMessage{"count": json.RawMessage(`9007199254740993`)},
		CreatedAt: createdAt,
		UpdatedAt: createdAt,
		Version:   math.MaxUint64 - 1,
	}
	if err := store.Create(ctx, want); err != nil {
		t.Fatal(err)
	}

	want.Scope = "project"
	want.Content = "corrected"
	want.UpdatedAt = createdAt.Add(time.Hour)
	want.Version++

	if err := store.Update(ctx, want, want.Version-1); err != nil {
		t.Fatal(err)
	}

	reopened, err := sqlite.New(openDatabase(t, path))
	if err != nil {
		t.Fatal(err)
	}

	got, err := reopened.Get(ctx, want.ID)
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("reopened entry = %+v, %v, want %+v", got, err, want)
	}

	if err := reopened.Delete(ctx, want.ID, want.Version); err != nil {
		t.Fatal(err)
	}

	if _, err := store.Get(ctx, want.ID); !errors.Is(err, memory.ErrNotFound) {
		t.Fatalf("Get(deleted) error = %v, want ErrNotFound", err)
	}
}

func TestStoreListsFilteredPages(t *testing.T) {
	t.Parallel()

	store, err := sqlite.New(openDatabase(t, filepath.Join(t.TempDir(), "entries.db")))
	if err != nil {
		t.Fatal(err)
	}

	for _, entry := range []memory.Entry{
		{ID: "c", Scope: "personal", Version: 1},
		{ID: "a", Scope: "project", Version: 1},
		{ID: "b", Scope: "personal", Version: 1},
		{ID: "x' OR 1=1 --", Scope: "scope' ?", Version: 1},
	} {
		if err := store.Create(t.Context(), entry); err != nil {
			t.Fatal(err)
		}
	}

	for _, test := range []struct {
		filter memory.Filter
		want   []string
		nextID string
	}{
		{filter: memory.Filter{Limit: 2}, want: []string{"a", "b"}, nextID: "b"},
		{filter: memory.Filter{AfterID: "b"}, want: []string{"c", "x' OR 1=1 --"}},
		{filter: memory.Filter{Scopes: []string{"personal"}}, want: []string{"b", "c"}},
		{filter: memory.Filter{IDs: []string{"c", "a", "missing"}}, want: []string{"a", "c"}},
		{filter: memory.Filter{Scopes: []string{"scope' ?"}}, want: []string{"x' OR 1=1 --"}},
	} {
		page, err := store.List(t.Context(), test.filter)
		if err != nil {
			t.Fatal(err)
		}

		got := make([]string, 0, len(page.Entries))
		for _, entry := range page.Entries {
			got = append(got, entry.ID)
		}

		if !slices.Equal(got, test.want) || page.NextID != test.nextID {
			t.Fatalf("List(%+v) = %v next %q, want %v next %q", test.filter, got, page.NextID, test.want, test.nextID)
		}
	}
}

func TestStoreRejectsStaleAndMissingWrites(t *testing.T) {
	t.Parallel()

	store, err := sqlite.New(openDatabase(t, filepath.Join(t.TempDir(), "entries.db")))
	if err != nil {
		t.Fatal(err)
	}

	original := memory.Entry{ID: "entry", Content: "original", Version: 2}
	if err := store.Create(t.Context(), original); err != nil {
		t.Fatal(err)
	}

	if err := store.Update(t.Context(), memory.Entry{ID: "entry", Content: "stale", Version: 3}, 1); !errors.Is(err, memory.ErrConflict) {
		t.Fatalf("Update(stale) error = %v, want ErrConflict", err)
	}

	if err := store.Delete(t.Context(), "entry", 1); !errors.Is(err, memory.ErrConflict) {
		t.Fatalf("Delete(stale) error = %v, want ErrConflict", err)
	}

	if err := store.Update(t.Context(), memory.Entry{ID: "missing", Version: 2}, 1); !errors.Is(err, memory.ErrNotFound) {
		t.Fatalf("Update(missing) error = %v, want ErrNotFound", err)
	}

	got, err := store.Get(t.Context(), original.ID)
	if err != nil || !reflect.DeepEqual(got, original) {
		t.Fatalf("rejected writes changed the entry: %+v, %v", got, err)
	}
}

func TestStoreConditionalWritesAcrossConnections(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "entries.db")

	var stores []memory.Store
	for range 2 {
		store, err := sqlite.New(openDatabase(t, path))
		if err != nil {
			t.Fatal(err)
		}

		stores = append(stores, store)
	}

	for _, test := range []struct {
		name  string
		write func(context.Context, memory.Store) error
		want  error
	}{
		{
			name: "create",
			write: func(ctx context.Context, store memory.Store) error {
				return store.Create(ctx, memory.Entry{ID: "shared", Content: "original", Version: 1})
			},
			want: memory.ErrAlreadyExists,
		},
		{
			name: "update",
			write: func(ctx context.Context, store memory.Store) error {
				return store.Update(ctx, memory.Entry{ID: "shared", Content: "updated", Version: 2}, 1)
			},
			want: memory.ErrConflict,
		},
		{
			name: "delete",
			write: func(ctx context.Context, store memory.Store) error {
				return store.Delete(ctx, "shared", 2)
			},
			want: memory.ErrNotFound,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			var (
				successes atomic.Int32
				rejected  atomic.Int32
				workers   sync.WaitGroup
			)
			for position := range 24 {
				workers.Go(func() {
					err := test.write(t.Context(), stores[position%len(stores)])
					switch {
					case err == nil:
						successes.Add(1)
					case errors.Is(err, test.want):
						rejected.Add(1)
					default:
						t.Errorf("concurrent write = %v", err)
					}
				})
			}

			workers.Wait()

			if successes.Load() != 1 || rejected.Load() != 23 {
				t.Fatalf("successful writes = %d, rejected writes = %d", successes.Load(), rejected.Load())
			}
		})
	}
}

func TestStoreFailedWritesPreserveEntries(t *testing.T) {
	t.Parallel()

	db := openDatabase(t, filepath.Join(t.TempDir(), "entries.db"))

	store, err := sqlite.New(db)
	if err != nil {
		t.Fatal(err)
	}

	original := memory.Entry{ID: "entry", Scope: "personal", Content: "original", Version: 1}
	if err := store.Create(t.Context(), original); err != nil {
		t.Fatal(err)
	}

	for _, test := range []struct {
		name    string
		trigger string
		write   func(context.Context) error
	}{
		{
			name:    "create",
			trigger: "BEFORE INSERT",
			write: func(ctx context.Context) error {
				return store.Create(ctx, memory.Entry{ID: "new", Scope: "changed", Version: 1})
			},
		},
		{
			name:    "update",
			trigger: "BEFORE UPDATE",
			write: func(ctx context.Context) error {
				return store.Update(ctx, memory.Entry{ID: "entry", Scope: "changed", Content: "replacement", Version: 2}, 1)
			},
		},
		{
			name:    "delete",
			trigger: "BEFORE DELETE",
			write: func(ctx context.Context) error {
				return store.Delete(ctx, "entry", 1)
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			query := "CREATE TRIGGER fail_write " + test.trigger + " ON entries BEGIN SELECT RAISE(ABORT, 'injected failure'); END"
			if _, err := db.ExecContext(t.Context(), query); err != nil {
				t.Fatal(err)
			}

			t.Cleanup(func() {
				if _, err := db.Exec("DROP TRIGGER fail_write"); err != nil {
					t.Error(err)
				}
			})

			if err := test.write(t.Context()); err == nil {
				t.Fatal("database failure was ignored")
			}

			got, err := store.Get(t.Context(), original.ID)
			if err != nil || !reflect.DeepEqual(got, original) {
				t.Fatalf("failed write changed the entry: %+v, %v", got, err)
			}

			page, err := store.List(t.Context(), memory.Filter{})
			if err != nil || len(page.Entries) != 1 || page.Entries[0].ID != original.ID {
				t.Fatalf("failed write changed enumeration: %+v, %v", page, err)
			}
		})
	}
}

func TestStorePropagatesInvalidStoredDataWithoutPartialResults(t *testing.T) {
	t.Parallel()

	for _, assignment := range []string{
		"metadata = x'7b'",
		"created_at = 'invalid'",
		"updated_at = 'invalid'",
		"version = 'invalid'",
	} {
		t.Run(assignment, func(t *testing.T) {
			db := openDatabase(t, filepath.Join(t.TempDir(), "entries.db"))

			store, err := sqlite.New(db)
			if err != nil {
				t.Fatal(err)
			}

			for _, id := range []string{"a", "b"} {
				if err := store.Create(t.Context(), memory.Entry{ID: id, Version: 1}); err != nil {
					t.Fatal(err)
				}
			}

			if _, err := db.ExecContext(t.Context(), "UPDATE entries SET "+assignment+" WHERE id = 'b'"); err != nil {
				t.Fatal(err)
			}

			if entry, err := store.Get(t.Context(), "b"); err == nil || entry.ID != "" {
				t.Fatalf("invalid data was returned: %+v, %v", entry, err)
			}

			if page, err := store.List(t.Context(), memory.Filter{}); err == nil || len(page.Entries) != 0 {
				t.Fatalf("invalid data produced partial results: %+v, %v", page, err)
			}
		})
	}
}

func TestNewLeavesDatabaseLifetimeToCaller(t *testing.T) {
	t.Parallel()

	db := openDatabase(t, filepath.Join(t.TempDir(), "entries.db"))
	if _, err := db.Exec("CREATE VIEW entries AS SELECT 1"); err != nil {
		t.Fatal(err)
	}

	if store, err := sqlite.New(db); err == nil || store != nil {
		t.Fatalf("invalid schema was accepted: %v, %v", store, err)
	}

	if err := db.Ping(); err != nil {
		t.Fatalf("failed construction closed the database: %v", err)
	}

	if _, err := db.Exec("DROP VIEW entries"); err != nil {
		t.Fatal(err)
	}

	store, err := sqlite.New(db)
	if err != nil {
		t.Fatal(err)
	}

	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	if _, err := store.Get(t.Context(), "entry"); err == nil {
		t.Fatal("read succeeded after the caller closed the database")
	}
}

func openDatabase(t *testing.T, path string) *sql.DB {
	t.Helper()

	dsn := url.URL{Scheme: "file", Path: filepath.ToSlash(path)}
	parameters := url.Values{}
	parameters.Add("_pragma", "busy_timeout(5000)")
	parameters.Add("_pragma", "journal_mode(WAL)")
	parameters.Add("_pragma", "synchronous(FULL)")
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
