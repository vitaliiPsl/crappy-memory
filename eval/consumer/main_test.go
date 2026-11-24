package main

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/vitaliipsl/crappy-memory/index/bm25"
	"github.com/vitaliipsl/crappy-memory/memory"
	"github.com/vitaliipsl/crappy-memory/retrieval"
)

func TestConsumerWorkflow(t *testing.T) {
	t.Parallel()

	for _, backend := range []string{"json", "sqlite"} {
		t.Run(backend, func(t *testing.T) {
			t.Parallel()

			var output bytes.Buffer
			if err := run(t.Context(), backend, &output); err != nil {
				t.Fatal(err)
			}

			if !strings.Contains(output.String(), "Prefers table-driven tests in Go. (version 2)") {
				t.Fatalf("unexpected consumer output: %s", &output)
			}
		})
	}
}

func TestConsumerRecoversAfterIndexFailures(t *testing.T) {
	t.Parallel()

	for _, backend := range []string{"json", "sqlite"} {
		t.Run(backend, func(t *testing.T) {
			t.Parallel()

			store, closeStore, err := openStore(backend, t.TempDir())
			if err != nil {
				t.Fatal(err)
			}

			t.Cleanup(func() { _ = closeStore() })

			index, err := bm25.New(memory.Items(t.Context(), store))
			if err != nil {
				t.Fatal(err)
			}

			var failures []error

			memories := memory.New(store,
				memory.WithIndexer(newUnavailableIndexer(), func(_ context.Context, err error) { failures = append(failures, err) }),
				memory.WithRetriever(index),
			)

			created, err := memories.Create(t.Context(), "personal", "Use Go table-driven tests.")
			if err != nil || len(failures) != 1 {
				t.Fatalf("canonical write: entry=%+v, error=%v, indexing failures=%v", created, err, failures)
			}

			if err := index.Upsert(t.Context(), retrieval.Item{ID: created.ID, Scope: created.Scope, Version: created.Version, Text: created.Content}); err != nil {
				t.Fatal(err)
			}

			updated, err := memories.Update(t.Context(), created.ID, created.Version, "Use Go fuzz tests.")
			if err != nil || len(failures) != 2 {
				t.Fatalf("canonical update: entry=%+v, error=%v, indexing failures=%v", updated, err, failures)
			}

			results, err := memories.Search(t.Context(), "Go tests")
			if err != nil || len(results) != 0 {
				t.Fatalf("outdated index returned stale content: %+v, %v", results, err)
			}

			recovered, err := openMemory(t.Context(), store)
			if err != nil {
				t.Fatal(err)
			}

			results, err = recovered.Search(t.Context(), "fuzz")
			if err != nil || len(results) != 1 || results[0].Entry.Content != updated.Content {
				t.Fatalf("rebuilt index lost the canonical update: %+v, %v", results, err)
			}

			if err := memories.Delete(t.Context(), updated.ID, updated.Version); err != nil || len(failures) != 3 {
				t.Fatalf("canonical deletion: error=%v, indexing failures=%v", err, failures)
			}

			results, err = recovered.Search(t.Context(), "fuzz")
			if err != nil || len(results) != 0 {
				t.Fatalf("failed removal exposed a deleted entry: %+v, %v", results, err)
			}
		})
	}
}

type unavailableIndexer struct{}

func newUnavailableIndexer() *unavailableIndexer {
	return &unavailableIndexer{}
}

func (*unavailableIndexer) Upsert(context.Context, retrieval.Item) error {
	return errors.New("index unavailable")
}

func (*unavailableIndexer) Remove(context.Context, string) error {
	return errors.New("index unavailable")
}
