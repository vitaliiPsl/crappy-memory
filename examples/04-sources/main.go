package main

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"

	_ "modernc.org/sqlite"

	"github.com/vitaliipsl/crappy-memory/index/bm25"
	"github.com/vitaliipsl/crappy-memory/retrieval"
	"github.com/vitaliipsl/crappy-memory/source"
	"github.com/vitaliipsl/crappy-memory/source/chunking/recursive"
	sourcesqlite "github.com/vitaliipsl/crappy-memory/source/storage/sqlite"
)

const markdown = "text/markdown"

func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}

func run() error {
	directory, err := os.MkdirTemp("", "memory-sources-example-*")
	if err != nil {
		return err
	}

	defer func() { _ = os.RemoveAll(directory) }()

	ctx := context.Background()

	db, err := sql.Open("sqlite", filepath.Join(directory, "sources.db"))
	if err != nil {
		return err
	}

	defer func() { _ = db.Close() }()

	db.SetMaxOpenConns(1)

	store, err := sourcesqlite.New(db)
	if err != nil {
		return err
	}

	keywords, err := bm25.New(source.Items(ctx, store))
	if err != nil {
		return err
	}

	chunker, err := recursive.New(recursive.WithSize(70))
	if err != nil {
		return err
	}

	sources, err := source.New(ctx, store,
		source.WithChunker(markdown, chunker),
		source.WithIndexer(keywords, onIndexError),
		source.WithRetriever(keywords),
	)
	if err != nil {
		return err
	}

	for _, document := range []struct{ id, content string }{
		{"docs/setup.md", "# Setup\n\n## Install\n\nRun `make install` to build the binary.\n\n## Configure\n\nSet `PORT` before starting the server.\n"},
		{"docs/release.md", "# Release\n\nPush a version tag to publish a release.\n"},
	} {
		if _, err := sources.Put(ctx, document.id, "project:website", markdown, []byte(document.content)); err != nil {
			return err
		}
	}

	if err := search(ctx, sources, "how do I install it"); err != nil {
		return err
	}

	edited := "# Setup\n\n## Install\n\nRun `go install ./cmd/server` instead.\n\n## Configure\n\nSet `PORT` before starting the server.\n"
	if _, err := sources.Put(ctx, "docs/setup.md", "project:website", markdown, []byte(edited)); err != nil {
		return err
	}

	if err := search(ctx, sources, "how do I install it"); err != nil {
		return err
	}

	if err := sources.Delete(ctx, "docs/release.md"); err != nil {
		return err
	}

	return search(ctx, sources, "publish a release")
}

func search(ctx context.Context, sources *source.Sources, query string) error {
	results, err := sources.Search(ctx, query, retrieval.WithScopes("project:website"), retrieval.WithLimit(2))
	if err != nil {
		return err
	}

	fmt.Printf("%q\n", query)

	if len(results) == 0 {
		fmt.Println("  no passages")
	}

	for _, result := range results {
		fmt.Printf("  %s, %s: %s\n", result.Chunk.DocumentID, result.Chunk.Location, strings.ReplaceAll(result.Chunk.Content, "\n", " "))
	}

	return nil
}

func onIndexError(_ context.Context, err error) {
	log.Printf("source indexing: %v", err)
}
