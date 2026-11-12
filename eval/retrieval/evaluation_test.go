package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"slices"
	"testing"
	"time"

	"github.com/vitaliipsl/crappy-memory/index/vector"
)

func TestScore(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name       string
		relevant   []string
		retrieved  []string
		limit      int
		recall     float64
		precision  float64
		reciprocal float64
	}{
		{name: "no matches", relevant: []string{"a"}, retrieved: []string{"x"}, limit: 3},
		{name: "empty results", relevant: []string{"a"}, limit: 3},
		{name: "partial recall", relevant: []string{"a", "b"}, retrieved: []string{"a", "x"}, limit: 3, recall: 0.5, precision: 1.0 / 3, reciprocal: 1},
		{name: "late first match", relevant: []string{"a", "b"}, retrieved: []string{"x", "b", "a"}, limit: 3, recall: 1, precision: 2.0 / 3, reciprocal: 0.5},
		{name: "duplicate hit", relevant: []string{"a", "b"}, retrieved: []string{"a", "a"}, limit: 3, recall: 0.5, precision: 1.0 / 3, reciprocal: 1},
		{name: "fewer results than k", relevant: []string{"a"}, retrieved: []string{"a"}, limit: 3, recall: 1, precision: 1.0 / 3, reciprocal: 1},
		{name: "outside cutoff", relevant: []string{"a"}, retrieved: []string{"x", "a"}, limit: 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			recall, precision, reciprocal := score(test.relevant, test.retrieved, test.limit)
			if recall != test.recall || precision != test.precision || reciprocal != test.reciprocal {
				t.Fatalf("score = (%v, %v, %v), want (%v, %v, %v)", recall, precision, reciprocal, test.recall, test.precision, test.reciprocal)
			}
		})
	}
}

func TestEvaluateDatasetComposesThreeRetrievalModes(t *testing.T) {
	t.Parallel()

	corpus := dataset{
		Entries: []document{
			{ID: "a", Scope: "project", Content: "alpha notes"},
			{ID: "b", Scope: "project", Content: "beta notes"},
			{ID: "c", Scope: "other", Content: "alpha notes"},
		},
		Queries: []query{{ID: "q", Text: "alpha", Scopes: []string{"project"}, Relevant: []string{"a"}}},
	}

	embedder := newFixtureEmbedder(map[string][]float32{
		"alpha notes": {1, 0},
		"beta notes":  {0, 1},
		"alpha":       {1, 0},
	})

	reports, err := evaluateDataset(t.Context(), corpus, embedder, 1, 3)
	if err != nil {
		t.Fatal(err)
	}

	if len(reports) != 3 {
		t.Fatalf("expected three retrieval modes, got %d", len(reports))
	}

	for position, mode := range []string{"bm25", "vector", "hybrid"} {
		report := reports[position]
		if report.Mode != mode || report.Recall != 1 || report.Precision != 1 || report.MRR != 1 || len(report.Queries[0].Retrieved) != 1 || report.Queries[0].Retrieved[0] != "a" {
			t.Fatalf("unexpected %s report: %+v", mode, report)
		}
	}

	if embedder.calls != 11 {
		t.Fatalf("expected three document embeddings and eight query embeddings for warm-up and measured passes, got %d", embedder.calls)
	}
}

func TestBundledBM25Baseline(t *testing.T) {
	t.Parallel()

	corpus, err := loadDataset(defaultDataset)
	if err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile("testdata/baseline.json")
	if err != nil {
		t.Fatal(err)
	}

	var baseline report
	if err := json.Unmarshal(data, &baseline); err != nil {
		t.Fatal(err)
	}

	if baseline.DatasetSHA256 != fmt.Sprintf("%x", sha256.Sum256(defaultDataset)) {
		t.Fatal("dataset changed without recording a new baseline")
	}

	reports, err := evaluateDataset(t.Context(), corpus, nil, baseline.K, 1)
	if err != nil {
		t.Fatal(err)
	}

	if len(reports) != 1 || reports[0].Mode != "bm25" {
		t.Fatalf("unexpected baseline reports: %+v", reports)
	}

	report := reports[0]

	expected := baseline.Evaluations[0]
	if expected.Mode != "bm25" || len(report.Queries) != len(expected.Queries) {
		t.Fatal("baseline must contain BM25 results for every query")
	}

	if math.Abs(report.Recall-expected.Recall) > 1e-9 || math.Abs(report.Precision-expected.Precision) > 1e-9 || math.Abs(report.MRR-expected.MRR) > 1e-9 {
		t.Fatalf("BM25 baseline changed: Recall=%v Precision=%v MRR=%v", report.Recall, report.Precision, report.MRR)
	}

	for position, result := range report.Queries {
		want := expected.Queries[position]
		if result.ID != want.ID || !slices.Equal(result.Retrieved, want.Retrieved) || result.Recall != want.Recall || result.Precision != want.Precision || result.Reciprocal != want.Reciprocal {
			t.Errorf("query %q changed: got %+v, want %+v", result.ID, result, want)
		}
	}
}

func TestSummarizeLatency(t *testing.T) {
	t.Parallel()

	mean, p95 := summarizeLatency([]time.Duration{3 * time.Millisecond, time.Millisecond, 2 * time.Millisecond})
	if mean != 2*time.Millisecond || p95 != 3*time.Millisecond {
		t.Fatalf("latency summary = (%v, %v), want (2ms, 3ms)", mean, p95)
	}

	samples := make([]time.Duration, 20)
	for position := range samples {
		samples[position] = time.Duration(19-position) * time.Millisecond
	}

	mean, p95 = summarizeLatency(samples)
	if mean != 9500*time.Microsecond || p95 != 18*time.Millisecond {
		t.Fatalf("latency summary = (%v, %v), want (9.5ms, 18ms)", mean, p95)
	}
}

type fixtureEmbedder struct {
	vectors map[string][]float32
	calls   int
}

func newFixtureEmbedder(vectors map[string][]float32) *fixtureEmbedder {
	return &fixtureEmbedder{vectors: vectors}
}

func (*fixtureEmbedder) Space() string {
	return "evaluation-fixture"
}

func (e *fixtureEmbedder) EmbedQuery(_ context.Context, text string) (vector.Embedding, error) {
	e.calls++

	values, exists := e.vectors[text]
	if !exists {
		return vector.Embedding{}, fmt.Errorf("no fixture embedding for %q", text)
	}

	return vector.Embedding{Values: values}, nil
}

func (e *fixtureEmbedder) EmbedDocuments(ctx context.Context, texts []string) ([]vector.Embedding, error) {
	embeddings := make([]vector.Embedding, len(texts))
	for position, text := range texts {
		embedding, err := e.EmbedQuery(ctx, text)
		if err != nil {
			return nil, err
		}

		embeddings[position] = embedding
	}

	return embeddings, nil
}
