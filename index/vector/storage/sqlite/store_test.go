package sqlite_test

import (
	"context"
	"database/sql"
	"errors"
	"math"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	_ "modernc.org/sqlite" // Register the SQLite test driver.

	"github.com/vitaliipsl/crappy-memory/index/vector"
	"github.com/vitaliipsl/crappy-memory/index/vector/storage/sqlite"
)

func TestStorePersistsRecordsAndSearchesAfterReopening(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	path := filepath.Join(t.TempDir(), "nested", "vectors ?#.db")
	store, db := newStore(t, path)

	records := []vector.Record{
		{ID: "a", Scope: "one", Version: 2, Space: "test-space", Values: []float32{2, 0}},
		{ID: "b", Scope: "two", Version: 1, Space: "test-space", Values: []float32{1, 0}},
		{ID: "c", Scope: "one", Version: 3, Space: "test-space", Values: []float32{1, 1}},
		{ID: "d", Scope: "one/child", Version: 1, Space: "test-space", Values: []float32{-1, 0}},
		{ID: "e", Scope: "one", Version: 1, Space: "other-space", Values: []float32{1, 0, 0}},
	}
	if err := store.Upsert(ctx, records); err != nil {
		t.Fatal(err)
	}

	reader, readerDB := newStore(t, path)
	for _, expected := range records {
		got, err := reader.Get(ctx, expected.ID)
		if err != nil || !reflect.DeepEqual(got, expected) {
			t.Fatalf("committed record = %+v, %v; want %+v", got, err, expected)
		}
	}

	if err := readerDB.Close(); err != nil {
		t.Fatal(err)
	}

	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	store, _ = newStore(t, path)

	results, err := store.Search(ctx, vector.Query{Space: "test-space", Values: []float32{1, 0}})
	if err != nil || len(results) != 4 || results[0].ID != "a" || results[1].ID != "b" || results[2].ID != "c" || results[3].ID != "d" {
		t.Fatalf("persisted cosine ranking = %+v, %v", results, err)
	}

	if results[0].Score != 1 || results[1].Score != 1 || math.Abs(results[2].Score-1/math.Sqrt(2)) > 1e-12 || results[3].Score != -1 {
		t.Fatalf("persisted cosine scores = %+v", results)
	}

	results, err = store.Search(ctx, vector.Query{Space: "test-space", Values: []float32{1, 0}, Scopes: []string{"one"}, IDs: []string{"b", "c", "d"}, Limit: 1})
	if err != nil || len(results) != 1 || results[0].ID != "c" || results[0].Version != 3 {
		t.Fatalf("filtered ranking = %+v, %v", results, err)
	}

	page, err := store.List(ctx, "", 2)
	if err != nil || len(page.Records) != 2 || page.Records[0].ID != "a" || page.Records[1].ID != "b" || page.NextID != "b" {
		t.Fatalf("first record page = %+v, %v", page, err)
	}

	page, err = store.List(ctx, page.NextID, 2)
	if err != nil || len(page.Records) != 2 || page.Records[0].ID != "c" || page.Records[1].ID != "d" || page.NextID != "d" {
		t.Fatalf("second record page = %+v, %v", page, err)
	}

	page, err = store.List(ctx, page.NextID, 2)
	if err != nil || len(page.Records) != 1 || page.Records[0].ID != "e" || page.NextID != "" {
		t.Fatalf("last record page = %+v, %v", page, err)
	}

	if _, err := store.List(ctx, "", 0); err == nil {
		t.Fatal("non-positive list limit accepted")
	}

	records[0].Values[0] = 9

	for range 2 {
		record, err := store.Get(ctx, "a")
		if err != nil || record.Values[0] != 2 {
			t.Fatalf("record data ownership = %+v, %v", record, err)
		}

		record.Values[0] = 9
	}
}

func TestStorePersistsReplacementsAndRemovals(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	path := filepath.Join(t.TempDir(), "vectors.db")

	store, db := newStore(t, path)
	if err := store.Upsert(ctx, []vector.Record{
		{ID: "note", Scope: "old", Version: 1, Space: "old", Values: []float32{1, 0}},
		{ID: "note", Scope: "new", Version: 2, Space: "old", Values: []float32{0, 1}},
	}); err != nil {
		t.Fatal(err)
	}

	expected := vector.Record{ID: "note", Scope: "new", Version: 2, Space: "new", Values: []float32{0, 1, 0}}
	if err := store.Upsert(ctx, []vector.Record{expected}); err != nil {
		t.Fatal(err)
	}

	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	store, db = newStore(t, path)

	record, err := store.Get(ctx, "note")
	if err != nil || !reflect.DeepEqual(record, expected) {
		t.Fatalf("persisted replacement = %+v, %v; want %+v", record, err, expected)
	}

	for _, id := range []string{"note", "note", "missing"} {
		if err := store.Remove(ctx, id); err != nil {
			t.Fatal(err)
		}
	}

	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	store, _ = newStore(t, path)
	if _, err := store.Get(ctx, "note"); !errors.Is(err, vector.ErrNotFound) {
		t.Fatalf("removed vector survived reopening: %v", err)
	}

	page, err := store.List(ctx, "", 10)
	if err != nil || len(page.Records) != 0 {
		t.Fatalf("removed vector remained listed: %+v, %v", page, err)
	}
}

func TestStorePersistsUnsignedVersions(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "vectors.db")

	store, db := newStore(t, path)
	if err := store.Upsert(t.Context(), []vector.Record{{ID: "note", Version: math.MaxUint64, Space: "space", Values: []float32{1}}}); err != nil {
		t.Fatal(err)
	}

	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	store, _ = newStore(t, path)

	record, err := store.Get(t.Context(), "note")
	if err != nil || record.Version != math.MaxUint64 || record.Values[0] != 1 {
		t.Fatalf("unsigned version round trip = %+v, %v", record, err)
	}
}

func TestStoreFiltersBeforeCustomScoring(t *testing.T) {
	t.Parallel()

	calls := 0

	store, _ := newStore(t, filepath.Join(t.TempDir(), "vectors.db"), sqlite.WithSimilarity(func(query, document []float32) float64 {
		calls++

		return float64(query[0])*float64(document[0]) + float64(query[1])*float64(document[1])
	}))
	if err := store.Upsert(t.Context(), []vector.Record{
		{ID: "near", Scope: "one", Version: 1, Space: "space", Values: []float32{1, 0}},
		{ID: "far", Scope: "one", Version: 1, Space: "space", Values: []float32{3, 4}},
		{ID: "other-scope", Scope: "two", Version: 1, Space: "space", Values: []float32{1, 0, 0}},
		{ID: "other-space", Scope: "one", Version: 1, Space: "other", Values: []float32{1, 0, 0}},
	}); err != nil {
		t.Fatal(err)
	}

	results, err := store.Search(t.Context(), vector.Query{Space: "space", Scopes: []string{"one"}, Values: []float32{2, 0}, Limit: 1})
	if err != nil || len(results) != 1 || results[0].ID != "far" || results[0].Score != 6 || calls != 2 {
		t.Fatalf("custom scoring = %+v, %v; calls=%d", results, err, calls)
	}

	results, err = store.Search(t.Context(), vector.Query{Space: "space", Values: []float32{1, 0}})
	if err == nil || results != nil {
		t.Fatalf("dimension mismatch produced partial results: %+v, %v", results, err)
	}
}

func TestStoreRejectsNonFiniteScores(t *testing.T) {
	t.Parallel()

	for _, score := range []float64{math.NaN(), math.Inf(1), math.Inf(-1)} {
		store, _ := newStore(t, filepath.Join(t.TempDir(), "vectors.db"), sqlite.WithSimilarity(func(_, _ []float32) float64 { return score }))
		if err := store.Upsert(t.Context(), []vector.Record{{ID: "note", Version: 1, Space: "space", Values: []float32{1}}}); err != nil {
			t.Fatal(err)
		}

		results, err := store.Search(t.Context(), vector.Query{Space: "space", Values: []float32{1}})
		if err == nil || results != nil {
			t.Fatalf("non-finite score produced results: %+v, %v", results, err)
		}
	}
}

func TestStoreRollsBackFailedBatches(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	path := filepath.Join(t.TempDir(), "vectors.db")

	store, db := newStore(t, path)
	if err := store.Upsert(ctx, []vector.Record{{ID: "existing", Version: 1, Space: "space", Values: []float32{1}}}); err != nil {
		t.Fatal(err)
	}

	for _, invalid := range []vector.Record{
		{Version: 1, Space: "space", Values: []float32{1}},
		{ID: "invalid", Space: "space", Values: []float32{1}},
		{ID: "invalid", Version: 1, Values: []float32{1}},
		{ID: "invalid", Version: 1, Space: "space"},
		{ID: "invalid", Version: 1, Space: "space", Values: []float32{0, 0}},
		{ID: "invalid", Version: 1, Space: "space", Values: []float32{float32(math.NaN())}},
		{ID: "invalid", Version: 1, Space: "space", Values: []float32{float32(math.Inf(1))}},
	} {
		if err := store.Upsert(ctx, []vector.Record{
			{ID: "existing", Version: 2, Space: "space", Values: []float32{2}},
			{ID: "new", Version: 1, Space: "space", Values: []float32{1}},
			invalid,
		}); err == nil {
			t.Fatalf("invalid record accepted: %+v", invalid)
		}
	}

	if _, err := db.Exec(`CREATE TRIGGER fail_insert BEFORE INSERT ON vectors WHEN NEW.id = 'failure' BEGIN SELECT RAISE(ABORT, 'injected failure'); END`); err != nil {
		t.Fatal(err)
	}

	if err := store.Upsert(ctx, []vector.Record{
		{ID: "existing", Version: 2, Space: "space", Values: []float32{2}},
		{ID: "failure", Version: 1, Space: "space", Values: []float32{1}},
	}); err == nil {
		t.Fatal("database failure was ignored")
	}

	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	store, _ = newStore(t, path)

	page, err := store.List(ctx, "", 10)
	if err != nil || len(page.Records) != 1 || page.Records[0].ID != "existing" || page.Records[0].Version != 1 || page.Records[0].Values[0] != 1 {
		t.Fatalf("failed batch committed partial writes: %+v, %v", page, err)
	}
}

func TestStorePropagatesRemovalFailures(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	path := filepath.Join(t.TempDir(), "vectors.db")
	store, db := newStore(t, path)

	expected := vector.Record{ID: "note", Version: 1, Space: "space", Values: []float32{1}}
	if err := store.Upsert(ctx, []vector.Record{expected}); err != nil {
		t.Fatal(err)
	}

	if _, err := db.Exec(`CREATE TRIGGER fail_remove BEFORE DELETE ON vectors BEGIN SELECT RAISE(ABORT, 'injected failure'); END`); err != nil {
		t.Fatal(err)
	}

	if err := store.Remove(ctx, "note"); err == nil {
		t.Fatal("database failure was ignored")
	}

	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	store, _ = newStore(t, path)

	record, err := store.Get(ctx, "note")
	if err != nil || !reflect.DeepEqual(record, expected) {
		t.Fatalf("failed removal changed stored data: %+v, %v", record, err)
	}
}

func TestStoreHonorsCancellation(t *testing.T) {
	t.Parallel()

	store, _ := newStore(t, filepath.Join(t.TempDir(), "vectors.db"))
	if err := store.Upsert(t.Context(), []vector.Record{{ID: "note", Version: 1, Space: "space", Values: []float32{1}}}); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	if err := store.Upsert(ctx, []vector.Record{{ID: "note", Version: 2, Space: "space", Values: []float32{2}}}); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled upsert = %v", err)
	}

	if err := store.Remove(ctx, "note"); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled removal = %v", err)
	}

	if _, err := store.Get(ctx, "note"); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled get = %v", err)
	}

	if _, err := store.List(ctx, "", 10); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled list = %v", err)
	}

	if _, err := store.Search(ctx, vector.Query{Space: "space", Values: []float32{1}}); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled search = %v", err)
	}

	record, err := store.Get(t.Context(), "note")
	if err != nil || record.Version != 1 {
		t.Fatalf("canceled writes changed the vector: %+v, %v", record, err)
	}
}

func TestNewLeavesDatabaseLifetimeToCaller(t *testing.T) {
	t.Parallel()

	db := openDatabase(t, filepath.Join(t.TempDir(), "vectors.db"))
	if _, err := db.Exec("CREATE VIEW vectors AS SELECT 1"); err != nil {
		t.Fatal(err)
	}

	if store, err := sqlite.New(db); err == nil || store != nil {
		t.Fatalf("invalid schema was accepted: %v, %v", store, err)
	}

	if err := db.Ping(); err != nil {
		t.Fatalf("failed construction closed the database: %v", err)
	}

	if _, err := db.Exec("DROP VIEW vectors"); err != nil {
		t.Fatal(err)
	}

	store, err := sqlite.New(db)
	if err != nil {
		t.Fatal(err)
	}

	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	if _, err := store.Get(t.Context(), "note"); err == nil {
		t.Fatal("read succeeded after the caller closed the database")
	}
}

func TestStoresInSeparateTablesShareADatabase(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	db := openDatabase(t, filepath.Join(t.TempDir(), "vectors.db"))

	entries, err := sqlite.New(db)
	if err != nil {
		t.Fatal(err)
	}

	chunks, err := sqlite.New(db, sqlite.WithTable("chunk_vectors"))
	if err != nil {
		t.Fatal(err)
	}

	if err := entries.Upsert(ctx, []vector.Record{{ID: "entry", Version: 1, Space: "space", Values: []float32{1, 0}}}); err != nil {
		t.Fatal(err)
	}

	if err := chunks.Upsert(ctx, []vector.Record{{ID: "chunk", Version: 1, Space: "space", Values: []float32{1, 0}}}); err != nil {
		t.Fatal(err)
	}

	for store, want := range map[*sqlite.Store]string{entries: "entry", chunks: "chunk"} {
		page, err := store.List(ctx, "", 10)
		if err != nil || len(page.Records) != 1 || page.Records[0].ID != want {
			t.Fatalf("table records = %+v, %v; want only %q", page, err, want)
		}

		results, err := store.Search(ctx, vector.Query{Space: "space", Values: []float32{1, 0}})
		if err != nil || len(results) != 1 || results[0].ID != want {
			t.Fatalf("table search = %+v, %v; want only %q", results, err, want)
		}
	}

	if err := chunks.Remove(ctx, "entry"); err != nil {
		t.Fatal(err)
	}

	if _, err := entries.Get(ctx, "entry"); err != nil {
		t.Fatalf("removal from one table changed another: %v", err)
	}

	for _, table := range []string{"", "1vectors", "chunk-vectors", `vectors"; DROP TABLE vectors; --`} {
		if store, err := sqlite.New(db, sqlite.WithTable(table)); store != nil || err == nil {
			t.Fatalf("invalid table name %q accepted: %v, %v", table, store, err)
		}
	}
}

func newStore(t *testing.T, path string, options ...sqlite.Option) (*sqlite.Store, *sql.DB) {
	t.Helper()

	db := openDatabase(t, path)

	store, err := sqlite.New(db, options...)
	if err != nil {
		t.Fatal(err)
	}

	return store, db
}

func openDatabase(t *testing.T, path string) *sql.DB {
	t.Helper()

	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}

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
