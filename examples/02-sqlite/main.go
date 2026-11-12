package main

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"os"
	"path/filepath"

	_ "modernc.org/sqlite"

	"github.com/vitaliipsl/crappy-memory/memory"
	sqlitestore "github.com/vitaliipsl/crappy-memory/storage/sqlite"
)

func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}

func run() error {
	directory, err := os.MkdirTemp("", "memory-sqlite-example-*")
	if err != nil {
		return err
	}

	defer func() { _ = os.RemoveAll(directory) }()

	ctx := context.Background()
	path := filepath.Join(directory, "memory.db")

	created, err := rememberDecision(ctx, path)
	if err != nil {
		return err
	}

	db, err := sql.Open("sqlite", path)
	if err != nil {
		return err
	}

	defer func() { _ = db.Close() }()

	db.SetMaxOpenConns(1)

	store, err := sqlitestore.New(db)
	if err != nil {
		return err
	}

	memories := memory.New(store)

	entry, err := memories.Get(ctx, created.ID)
	if err != nil {
		return err
	}

	fmt.Printf("Recalled after reopening: %s (version %d)\n", entry.Content, entry.Version)

	page, err := memories.List(ctx, memory.Filter{Scopes: []string{"project:website"}})
	if err != nil {
		return err
	}

	for _, entry := range page.Entries {
		fmt.Printf("%s: %s\n", entry.Scope, entry.Content)
	}

	if err := memories.Delete(ctx, entry.ID, entry.Version); err != nil {
		return err
	}

	fmt.Println("Forgot the saved decision.")

	return nil
}

func rememberDecision(ctx context.Context, path string) (memory.Entry, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return memory.Entry{}, err
	}

	defer func() { _ = db.Close() }()

	db.SetMaxOpenConns(1)

	store, err := sqlitestore.New(db)
	if err != nil {
		return memory.Entry{}, err
	}

	memories := memory.New(store)

	return memories.Create(ctx, "project:website", "Use SQLite for local development.",
		memory.WithSource("decision:local-storage"),
		memory.WithMetadata("category", "decision"),
	)
}
