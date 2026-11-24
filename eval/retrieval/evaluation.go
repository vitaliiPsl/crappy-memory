package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"time"

	"github.com/vitaliipsl/crappy-memory/index/bm25"
	"github.com/vitaliipsl/crappy-memory/index/vector"
	"github.com/vitaliipsl/crappy-memory/index/vector/storage/inmemory"
	"github.com/vitaliipsl/crappy-memory/memory"
	"github.com/vitaliipsl/crappy-memory/retrieval"
	jsonstore "github.com/vitaliipsl/crappy-memory/storage/json"
)

type evaluation struct {
	Mode        string        `json:"mode"`
	Recall      float64       `json:"recall"`
	Precision   float64       `json:"precision"`
	MRR         float64       `json:"mrr"`
	MeanLatency time.Duration `json:"mean_latency_ns"`
	P95Latency  time.Duration `json:"p95_latency_ns"`
	Queries     []queryResult `json:"queries"`
}

type queryResult struct {
	ID         string        `json:"id"`
	Retrieved  []string      `json:"retrieved"`
	Recall     float64       `json:"recall"`
	Precision  float64       `json:"precision"`
	Reciprocal float64       `json:"reciprocal"`
	Latency    time.Duration `json:"mean_latency_ns"`
}

func evaluateDataset(ctx context.Context, corpus dataset, embedder vector.Embedder, limit, runs int) ([]evaluation, error) {
	if limit <= 0 {
		return nil, errors.New("result limit must be positive")
	}

	if runs <= 0 {
		return nil, errors.New("measured runs must be positive")
	}

	directory, err := os.MkdirTemp("", "memory-retrieval-*")
	if err != nil {
		return nil, err
	}

	defer func() { _ = os.RemoveAll(directory) }()

	store, err := jsonstore.New(filepath.Join(directory, "entries.json"))
	if err != nil {
		return nil, err
	}

	memories := memory.New(store)
	for _, entry := range corpus.Entries {
		if _, err := memories.Create(ctx, entry.Scope, entry.Content, memory.WithSource(entry.ID)); err != nil {
			return nil, err
		}
	}

	keywords, err := bm25.New(memory.Items(ctx, store))
	if err != nil {
		return nil, err
	}

	baseline, err := evaluate(ctx, "bm25", memory.New(store, memory.WithRetriever(keywords)), corpus.Queries, limit, runs)
	if err != nil {
		return nil, err
	}

	if embedder == nil {
		return []evaluation{baseline}, nil
	}

	semantic, err := vector.New(ctx, memory.Items(ctx, store), inmemory.New(), embedder)
	if err != nil {
		return nil, err
	}

	rrf, err := retrieval.NewRRF()
	if err != nil {
		return nil, err
	}

	hybrid, err := retrieval.NewPipeline(retrieval.WithRetrievers(keywords, semantic), retrieval.WithMerger(rrf))
	if err != nil {
		return nil, err
	}

	reports := []evaluation{baseline}
	for _, mode := range []struct {
		name      string
		retriever retrieval.Retriever
	}{
		{name: "vector", retriever: semantic},
		{name: "hybrid", retriever: hybrid},
	} {
		report, err := evaluate(ctx, mode.name, memory.New(store, memory.WithRetriever(mode.retriever)), corpus.Queries, limit, runs)
		if err != nil {
			return nil, err
		}

		reports = append(reports, report)
	}

	return reports, nil
}

func evaluate(ctx context.Context, mode string, memories *memory.Memory, queries []query, limit, runs int) (evaluation, error) {
	for _, query := range queries {
		if _, err := searchQuery(ctx, memories, query, limit); err != nil {
			return evaluation{}, fmt.Errorf("%s query %q: %w", mode, query.ID, err)
		}
	}

	report := evaluation{Mode: mode, Queries: make([]queryResult, len(queries))}

	samples := make([]time.Duration, 0, len(queries)*runs)
	for run := range runs {
		for position, query := range queries {
			result, err := searchQuery(ctx, memories, query, limit)
			if err != nil {
				return evaluation{}, fmt.Errorf("%s query %q: %w", mode, query.ID, err)
			}

			samples = append(samples, result.Latency)
			if run == 0 {
				report.Queries[position] = result
			} else {
				report.Queries[position].Latency += result.Latency
			}
		}
	}

	for position := range report.Queries {
		result := &report.Queries[position]
		result.Latency /= time.Duration(runs)
		report.Recall += result.Recall
		report.Precision += result.Precision
		report.MRR += result.Reciprocal
	}

	report.Recall /= float64(len(queries))
	report.Precision /= float64(len(queries))
	report.MRR /= float64(len(queries))
	report.MeanLatency, report.P95Latency = summarizeLatency(samples)

	return report, nil
}

func searchQuery(ctx context.Context, memories *memory.Memory, query query, limit int) (queryResult, error) {
	started := time.Now()
	results, err := memories.Search(ctx, query.Text, retrieval.WithScopes(query.Scopes...), retrieval.WithLimit(limit))
	elapsed := time.Since(started)

	if err != nil {
		return queryResult{}, err
	}

	ids := make([]string, 0, len(results))
	for _, result := range results {
		if len(query.Scopes) != 0 && !slices.Contains(query.Scopes, result.Entry.Scope) {
			return queryResult{}, fmt.Errorf("entry %q is outside the query's scopes", result.Entry.Source)
		}

		ids = append(ids, result.Entry.Source)
	}

	recall, precision, reciprocal := score(query.Relevant, ids, limit)

	return queryResult{ID: query.ID, Retrieved: ids, Recall: recall, Precision: precision, Reciprocal: reciprocal, Latency: elapsed}, nil
}

func score(relevant, retrieved []string, limit int) (float64, float64, float64) {
	seen := make(map[string]bool)

	var reciprocal float64
	for rank, id := range retrieved[:min(limit, len(retrieved))] {
		if !slices.Contains(relevant, id) {
			continue
		}

		seen[id] = true

		if reciprocal == 0 {
			reciprocal = 1 / float64(rank+1)
		}
	}

	return float64(len(seen)) / float64(len(relevant)), float64(len(seen)) / float64(limit), reciprocal
}

func summarizeLatency(samples []time.Duration) (time.Duration, time.Duration) {
	var total time.Duration
	for _, sample := range samples {
		total += sample
	}

	sorted := slices.Sorted(slices.Values(samples))
	p95 := sorted[(95*len(sorted)+99)/100-1]

	return total / time.Duration(len(samples)), p95
}
