package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"

	"github.com/vitaliipsl/crappy-memory/index/bm25"
	"github.com/vitaliipsl/crappy-memory/index/vector"
	"github.com/vitaliipsl/crappy-memory/index/vector/embedding/ollama"
	"github.com/vitaliipsl/crappy-memory/index/vector/storage/inmemory"
	"github.com/vitaliipsl/crappy-memory/memory"
	"github.com/vitaliipsl/crappy-memory/pipeline"
	jsonstore "github.com/vitaliipsl/crappy-memory/storage/json"
)

func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}

func run() error {
	directory, err := os.MkdirTemp("", "memory-hybrid-example-*")
	if err != nil {
		return err
	}

	defer func() { _ = os.RemoveAll(directory) }()

	ctx := context.Background()

	store, err := jsonstore.New(filepath.Join(directory, "memory.json"))
	if err != nil {
		return err
	}

	keywords, err := bm25.New(ctx, store)
	if err != nil {
		return err
	}

	embedder, err := ollama.New("qwen3-embedding:0.6b",
		ollama.WithQueryPrefix("Instruct: Retrieve a saved memory that answers the question.\nQuery: "),
	)
	if err != nil {
		return err
	}

	semantic, err := vector.New(ctx, store, inmemory.New(), embedder)
	if err != nil {
		return err
	}

	merger, err := pipeline.NewRRF()
	if err != nil {
		return err
	}

	search, err := pipeline.New(store,
		pipeline.WithRetrievers(keywords, semantic),
		pipeline.WithMerger(merger),
	)
	if err != nil {
		return err
	}

	memories := memory.New(store,
		memory.WithIndexer(keywords, onIndexError),
		memory.WithIndexer(semantic, onIndexError),
		memory.WithPipeline(search),
	)

	for _, content := range []string{
		"Keep answers concise and lead with the result.",
		"Use table-driven tests with named cases for Go packages.",
		"Schedule reminders using Europe/Lisbon local time.",
	} {
		if _, err := memories.Create(ctx, "personal", content); err != nil {
			return err
		}
	}

	query := "How verbose should your replies be?"
	fmt.Println("Query:", query)

	results, err := memories.Search(ctx, query, memory.WithScopes("personal"), memory.WithLimit(2))
	if err != nil {
		return err
	}

	for _, result := range results {
		fmt.Printf("%.3f %s\n", result.Score, result.Entry.Content)
	}

	return nil
}

func onIndexError(_ context.Context, err error) {
	log.Printf("indexing: %v", err)
}
