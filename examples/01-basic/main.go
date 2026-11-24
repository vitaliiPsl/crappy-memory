package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"

	"github.com/vitaliipsl/crappy-memory/index/bm25"
	"github.com/vitaliipsl/crappy-memory/memory"
	"github.com/vitaliipsl/crappy-memory/retrieval"
	jsonstore "github.com/vitaliipsl/crappy-memory/storage/json"
)

func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}

func run() error {
	directory, err := os.MkdirTemp("", "memory-example-*")
	if err != nil {
		return err
	}

	defer func() { _ = os.RemoveAll(directory) }()

	ctx := context.Background()

	store, err := jsonstore.New(filepath.Join(directory, "memory.json"))
	if err != nil {
		return err
	}

	index, err := bm25.New(memory.Items(ctx, store))
	if err != nil {
		return err
	}

	memories := memory.New(store,
		memory.WithIndexer(index, func(_ context.Context, err error) { log.Printf("indexing: %v", err) }),
		memory.WithRetriever(index),
	)

	entry, err := memories.Create(ctx, "personal", "Prefers example-based tests in Go.",
		memory.WithSource("conversation:42"),
		memory.WithMetadata("category", "preference"),
	)
	if err != nil {
		return err
	}

	if _, err := memories.Create(ctx, "project:website", "Use Playwright for browser tests."); err != nil {
		return err
	}

	entry, err = memories.Get(ctx, entry.ID)
	if err != nil {
		return err
	}

	fmt.Printf("Remembered: %s (source: %s)\n", entry.Content, entry.Source)

	entry, err = memories.Update(ctx, entry.ID, entry.Version, "Prefers table-driven tests in Go.")
	if err != nil {
		return err
	}

	fmt.Printf("Updated to version %d: %s\n", entry.Version, entry.Content)

	page, err := memories.List(ctx, memory.Filter{Scopes: []string{"personal"}})
	if err != nil {
		return err
	}

	fmt.Printf("Personal memories: %d\n", len(page.Entries))

	results, err := memories.Search(ctx, "Go tests", retrieval.WithScopes("personal"), retrieval.WithLimit(3))
	if err != nil {
		return err
	}

	for _, result := range results {
		fmt.Printf("Recall: %.3f %s\n", result.Score, result.Entry.Content)
	}

	if err := memories.Delete(ctx, entry.ID, entry.Version); err != nil {
		return err
	}

	results, err = memories.Search(ctx, "Go tests", retrieval.WithScopes("personal"))
	if err != nil {
		return err
	}

	fmt.Printf("Recall after forgetting: %d results\n", len(results))

	return nil
}
