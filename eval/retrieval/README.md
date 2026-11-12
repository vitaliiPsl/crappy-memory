# Retrieval evaluation

A synthetic corpus of 100 entries and 40 queries covering exact identifiers, paraphrases, multiple relevant entries, historical instructions, and competing memories across scopes.

Run BM25, or compare it with vector and hybrid search using an installed Ollama model:

```sh
go run ./eval/retrieval
go run ./eval/retrieval -model qwen3-embedding:0.6b \
  -query-prefix $'Instruct: Retrieve a saved memory that answers the question.\nQuery: '
```

Evaluation uses temporary JSON storage, an in-memory vector store, and default pipeline settings: 100 candidates per retriever and RRF rank constant 60.

## Dataset and options

[testdata/retrieval.json](testdata/retrieval.json) contains:

- `entries`: unique `id`, `scope`, and `content` values.
- `queries`: unique `id`, query `text`, optional exact `scopes`, and `relevant` entry IDs.

An entry is relevant when it directly answers the question or a material part of it. Labels were authored before the model comparison and are not sent to the embedder.

Use `-dataset` for another corpus, `-k` for the result limit (default 3), and `-runs` for measured passes (default 3). Configure the embedder with `-ollama-url`, `-query-prefix`, and `-document-prefix`. Add `-json` to save a complete report with the dataset fingerprint and per-query results.

## Metrics

Quality metrics are averaged across queries:

- **Recall@k**: relevant entries found divided by all labeled relevant entries.
- **Precision@k**: relevant entries found divided by k, even when fewer results are returned.
- **MRR@k**: reciprocal rank of the first relevant result, or zero when none appears within k.

Latency reports the mean and nearest-rank p95 of `Memory.Search`, including query embedding. Each mode runs one warm-up pass followed by three measured passes: 120 samples by default. Quality comes from the first measured pass. Setup and initial document embedding are excluded. Text output uses milliseconds; JSON uses nanoseconds.

Tests compare BM25 rankings and quality metrics for each query with the saved baseline. Model tests use deterministic embeddings and need no Ollama server. Timings are informational.

## Baseline

Recorded at k=3 with three measured passes. Full report: [testdata/baseline.json](testdata/baseline.json).

| Retrieval | Recall@3 | Precision@3 | MRR@3 | Mean ms | P95 ms |
|-----------|---------:|------------:|------:|--------:|-------:|
| BM25 | 0.796 | 0.333 | 0.779 | 0.037 | 0.073 |
| Qwen vector | 0.942 | 0.392 | 0.879 | 27.575 | 29.378 |
| BM25 + Qwen with RRF | 0.867 | 0.367 | 0.850 | 30.188 | 41.478 |

The corpus has 50 relevance labels, so its maximum mean Precision@3 is 0.417. Vector search leads on this corpus; hybrid improves over BM25 but loses some vector-only matches.

Measured on Apple M5, Go 1.26.2 (darwin/arm64), and Ollama 0.34.0. The model was `qwen3-embedding:0.6b` Q8_0, digest `ac6da0dfba84a81fdbfbaf330198c33cd77c4cdfc53e8bc50eb581914a15621d`, using the query prefix above and no document prefix. Timings depend on hardware and server load.

Review per-query changes before replacing the baseline. These synthetic fixtures are a development baseline, not a held-out benchmark.
