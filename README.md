# Crappy Memory

<div align="center">

  [![Go](https://img.shields.io/badge/Go-1.26.2-00ADD8?style=flat&logo=go&logoColor=white)](https://golang.org)
  [![GoDoc](https://pkg.go.dev/badge/github.com/vitaliipsl/crappy-memory.svg)](https://pkg.go.dev/github.com/vitaliipsl/crappy-memory)
  [![License: MIT](https://img.shields.io/badge/license-MIT-blue.svg)](LICENSE)
</div>

A small long-term memory library for AI agents in Go. Durable entries, pluggable storage, keyword and vector indexes, and hybrid search.

## Motivation

Agents forget everything when a session ends. This library gives them durable memory without tying it to a particular model, agent framework, or database.

## Contents

- [Install](#install)
- [Quick start](#quick-start)
- [Memory](#memory)
- [Storage](#storage)
- [Search](#search)
- [Vector search](#vector-search)
- [Consistency](#consistency)
- [Extending](#extending)
- [Examples](#examples)
- [Validation](#validation)
- [License](#license)

## Install

```sh
go get github.com/vitaliipsl/crappy-memory
```

Requires Go 1.26.2.

API documentation: https://pkg.go.dev/github.com/vitaliipsl/crappy-memory

## Quick start

```go
ctx := context.Background()

store, err := jsonstore.New("memory.json")
if err != nil {
    log.Fatal(err)
}

index, err := bm25.New(ctx, store)
if err != nil {
    log.Fatal(err)
}

search, err := pipeline.New(store, pipeline.WithRetrievers(index))
if err != nil {
    log.Fatal(err)
}

memories := memory.New(store,
    memory.WithIndexer(index, func(_ context.Context, err error) {
        log.Printf("memory indexing: %v", err)
    }),
    memory.WithPipeline(search),
)

_, err = memories.Create(ctx, "personal", "Prefers table-driven tests in Go.")
if err != nil {
    log.Fatal(err)
}

results, err := memories.Search(ctx, "go tests", memory.WithScopes("personal"))
if err != nil {
    log.Fatal(err)
}

for _, result := range results {
    fmt.Printf("%.2f %s\n", result.Score, result.Entry.Content)
}
```

The index is passed twice: `WithIndexer` keeps it in sync with writes, and `WithRetrievers` lets search use it.

## Memory

An entry is one piece of knowledge: content in a scope, with an optional source and JSON metadata, timestamps, and a version. IDs are UUIDv7, so ID order is creation order. Scopes are flat namespaces such as `personal` or `project:website`; they don't nest, and access control belongs to the application.

```go
created, err := memories.Create(ctx, "personal", "Prefers tabs.",
    memory.WithSource("session:42"),
    memory.WithMetadata("category", "preference"),
)

updated, err := memories.Update(ctx, created.ID, created.Version, "Prefers spaces.")

err = memories.Delete(ctx, updated.ID, updated.Version)
```

Updates and deletes take the current version and return `ErrConflict` when it is stale or `ErrNotFound` when the entry is gone. `Get` reads one entry. `List` pages through entries in ID order, filtered by scopes or IDs; pass `page.NextID` as `AfterID` for the next page.

## Storage

| Store  | Package          | Persistence                                       |
|--------|------------------|---------------------------------------------------|
| JSON   | `storage/json`   | One JSON file, replaced atomically on every write |
| SQLite | `storage/sqlite` | An `entries` table in a caller-owned `*sql.DB`    |

The store is the source of truth, and indexes are rebuilt from it. The SQLite store borrows its database, so you choose the driver and configure and close it:

```go
db, err := sql.Open("sqlite", "file:memory.db?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)")
store, err := sqlitestore.New(db)
```

## Search

```go
results, err := memories.Search(ctx, "preferred test style",
    memory.WithScopes("personal", "project:website"),
    memory.WithLimit(5),
)
```

Search runs through a `pipeline.Pipeline`. Every retriever returns candidates, each candidate is loaded from the store, and deleted, outdated, or filtered-out entries are dropped. Results always carry the stored entry, so a stale index can hide an entry but never return old content.

`index/bm25` is an in-memory keyword index that `bm25.New` rebuilds from the store. It matches lowercase words without stemming, so `convention` doesn't match `conventions`.

Multiple retrievers need a merger. `pipeline.NewRRF()` provides reciprocal rank fusion. Scores come from the retriever, or from the merger when there are several, so compare them only within one setup.

## Vector search

`index/vector` embeds entries with an `Embedder`, keeps the vectors in a vector `Store`, and works as both indexer and retriever. Hybrid search with Ollama and SQLite:

```go
embedder, err := ollama.New("nomic-embed-text",
    ollama.WithQueryPrefix("search_query: "),
    ollama.WithDocumentPrefix("search_document: "),
)
vectors, err := vectorsqlite.New(db)
semantic, err := vector.New(ctx, store, vectors, embedder)
rrf, err := pipeline.NewRRF()

search, err := pipeline.New(store,
    pipeline.WithRetrievers(keywords, semantic),
    pipeline.WithMerger(rrf),
)

memories := memory.New(store,
    memory.WithIndexer(keywords, onIndexError),
    memory.WithIndexer(semantic, onIndexError),
    memory.WithPipeline(search),
)
```

Vectors live in memory (`index/vector/storage/inmemory`) or in a `vectors` table (`index/vector/storage/sqlite`) that can share the entry store's database. Both search exactly, using cosine similarity by default.

`vector.New` reconciles before returning. It drops vectors of deleted entries, embeds entries that are new, changed, or from another embedding space, and fails if any embedding fails. The Ollama space comes from the model name and prefixes, so changing them re-embeds everything. Truncation is off, so text longer than the model's context fails to embed.

## Consistency

- `Memory` writes to the store first, then updates every index. Index failures go to the indexer's handler and never fail the write; a missed update heals on the entry's next write or the next rebuild.
- Build indexes before constructing `Memory`, while nothing writes to the store.
- Serialize writes to the same entry. Out-of-order index updates leave an old version indexed, and search skips the entry until it changes again.
- Databases, embedders, and HTTP clients you pass in stay yours to configure and close.

## Extending

Everything is an interface. If something doesn't fit, replace it.

- **Store** — `memory.Store`
- **Indexer** — `memory.Indexer`
- **Retriever** — `memory.Retriever`, returning candidate IDs with their indexed version, or 0 if unknown
- **Merger** — `memory.Merger`
- **Pipeline** — `memory.Pipeline`
- **Embedder** — `vector.Embedder`
- **Vector store** — `vector.Store`
- **Similarity** — `vector.Similarity`

## Examples

| Example | Demonstrates |
|---------|--------------|
| [Basic memory](examples/01-basic) | Entry lifecycle, source and metadata, scoped listing, and BM25 recall |
| [SQLite](examples/02-sqlite) | Caller-owned database, persistence across reopening, scoped reads, and deletion |
| [Hybrid search](examples/03-hybrid) | Ollama embeddings, keyword and vector indexes, and RRF merging |

Examples use temporary storage. Hybrid search requires local Ollama with `qwen3-embedding:0.6b` installed.

```sh
go run ./examples/01-basic
go run ./examples/02-sqlite
go run ./examples/03-hybrid
```

## Validation

Consumer validation checks persistence, version conflicts, recall, and deletion with JSON and SQLite. Retrieval evaluation measures quality and latency across 100 entries and 40 labeled queries.

```sh
go run ./eval/consumer
go run ./eval/retrieval
go test -race ./...
golangci-lint run
```

See the [retrieval evaluation notes](eval/retrieval/README.md) for model configuration, metrics, and recorded results.

## License

MIT — see [LICENSE](LICENSE).
