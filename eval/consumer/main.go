package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"log"
	"net/url"
	"os"
	"path/filepath"

	_ "modernc.org/sqlite"

	"github.com/vitaliipsl/crappy-memory/index/bm25"
	"github.com/vitaliipsl/crappy-memory/memory"
	"github.com/vitaliipsl/crappy-memory/pipeline"
	jsonstore "github.com/vitaliipsl/crappy-memory/storage/json"
	sqlitestore "github.com/vitaliipsl/crappy-memory/storage/sqlite"
)

func main() {
	for _, backend := range []string{"json", "sqlite"} {
		if err := run(context.Background(), backend, os.Stdout); err != nil {
			log.Fatal(err)
		}
	}
}

func run(ctx context.Context, backend string, output io.Writer) error {
	directory, err := os.MkdirTemp("", "memory-example-*")
	if err != nil {
		return err
	}

	defer func() { _ = os.RemoveAll(directory) }()

	entry, err := learn(ctx, backend, directory)
	if err != nil {
		return err
	}

	return recallAndForget(ctx, backend, directory, entry, output)
}

func learn(ctx context.Context, backend, directory string) (memory.Entry, error) {
	store, closeStore, err := openStore(backend, directory)
	if err != nil {
		return memory.Entry{}, err
	}

	defer func() { _ = closeStore() }()

	memories, err := openMemory(ctx, store)
	if err != nil {
		return memory.Entry{}, err
	}

	created, err := memories.Create(ctx, "personal", "Prefers example-based tests in Go.",
		memory.WithSource("conversation:42"),
		memory.WithMetadata("category", "preference"),
	)
	if err != nil {
		return memory.Entry{}, err
	}

	updated, err := memories.Update(ctx, created.ID, created.Version, "Prefers table-driven tests in Go.")
	if err != nil {
		return memory.Entry{}, err
	}

	_, err = memories.Update(ctx, created.ID, created.Version, "This stale edit must fail.")
	if !errors.Is(err, memory.ErrConflict) {
		return memory.Entry{}, fmt.Errorf("stale update: expected a version conflict, got %v", err)
	}

	return updated, nil
}

func recallAndForget(ctx context.Context, backend, directory string, entry memory.Entry, output io.Writer) error {
	store, closeStore, err := openStore(backend, directory)
	if err != nil {
		return err
	}

	defer func() { _ = closeStore() }()

	memories, err := openMemory(ctx, store)
	if err != nil {
		return err
	}

	results, err := memories.Search(ctx, "Go tests", memory.WithScopes("personal"), memory.WithLimit(3))
	if err != nil {
		return err
	}

	if len(results) != 1 || results[0].Entry.ID != entry.ID || results[0].Entry.Version != entry.Version {
		return fmt.Errorf("recall after reopening: expected entry %s at version %d, got %v", entry.ID, entry.Version, results)
	}

	if _, err := fmt.Fprintf(output, "Recalled after reopening %s storage: %s (version %d)\n", backend, results[0].Entry.Content, results[0].Entry.Version); err != nil {
		return err
	}

	if err := memories.Delete(ctx, entry.ID, entry.Version); err != nil {
		return err
	}

	if _, err := memories.Get(ctx, entry.ID); !errors.Is(err, memory.ErrNotFound) {
		return fmt.Errorf("deleted entry: expected ErrNotFound, got %v", err)
	}

	results, err = memories.Search(ctx, "Go tests", memory.WithScopes("personal"))
	if err != nil {
		return err
	}

	if len(results) != 0 {
		return errors.New("deleted entry appeared in search")
	}

	_, err = fmt.Fprintln(output, "Stale update rejected; forgotten entry is absent from reads and search.")

	return err
}

func openMemory(ctx context.Context, store memory.Store) (*memory.Memory, error) {
	index, err := bm25.New(ctx, store)
	if err != nil {
		return nil, err
	}

	search, err := pipeline.New(store, pipeline.WithRetrievers(index))
	if err != nil {
		return nil, err
	}

	return memory.New(store,
		memory.WithIndexer(index, func(_ context.Context, err error) { log.Printf("indexing: %v", err) }),
		memory.WithPipeline(search),
	), nil
}

func openStore(backend, directory string) (memory.Store, func() error, error) {
	switch backend {
	case "json":
		store, err := jsonstore.New(filepath.Join(directory, "memory.json"))

		return store, func() error { return nil }, err
	case "sqlite":
		dsn := url.URL{Scheme: "file", Path: filepath.Join(directory, "memory.db")}
		parameters := url.Values{}
		parameters.Add("_pragma", "journal_mode(WAL)")
		parameters.Add("_pragma", "busy_timeout(5000)")
		dsn.RawQuery = parameters.Encode()

		db, err := sql.Open("sqlite", dsn.String())
		if err != nil {
			return nil, nil, err
		}

		db.SetMaxOpenConns(1)

		store, err := sqlitestore.New(db)
		if err != nil {
			return nil, nil, errors.Join(err, db.Close())
		}

		return store, db.Close, nil
	default:
		return nil, nil, fmt.Errorf("unknown storage %q", backend)
	}
}
