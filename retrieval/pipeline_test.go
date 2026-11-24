package retrieval_test

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/vitaliipsl/crappy-memory/retrieval"
)

func TestPipelineMergesRetrieverListsAndLimitsResults(t *testing.T) {
	t.Parallel()

	sources := [][]retrieval.Candidate{
		{{ID: "a", Version: 1, Score: 3}, {ID: "b", Version: 2, Score: 1}},
		{{ID: "b", Version: 2, Score: 9}},
	}

	var retrievers []retrieval.Retriever
	for _, list := range sources {
		retrievers = append(retrievers, retrieverFunc(func(_ context.Context, query retrieval.Query) ([]retrieval.Candidate, error) {
			if query.Text != "question" || query.Limit != 12 || !slices.Equal(query.Scopes, []string{"personal"}) || !slices.Equal(query.IDs, []string{"a", "b"}) {
				t.Fatalf("retriever query = %+v", query)
			}

			return list, nil
		}))
	}

	merger := mergerFunc(func(_ context.Context, lists [][]retrieval.Candidate) ([]retrieval.Candidate, error) {
		if !slices.EqualFunc(lists, sources, func(got, want []retrieval.Candidate) bool { return slices.Equal(got, want) }) {
			t.Fatalf("merged lists = %+v, want retriever order %+v", lists, sources)
		}

		return []retrieval.Candidate{{ID: "b", Version: 2, Score: 2}, {ID: "a", Version: 1, Score: 1}, {ID: "c", Version: 1}}, nil
	})
	pipeline := newPipeline(t, retrieval.WithRetrievers(retrievers...), retrieval.WithMerger(merger), retrieval.WithCandidateLimit(12))

	candidates, err := pipeline.Retrieve(t.Context(), retrieval.NewQuery("question",
		retrieval.WithScopes("personal"), retrieval.WithIDs("a", "b"), retrieval.WithLimit(2),
	))
	if err != nil || !slices.Equal(candidates, []retrieval.Candidate{{ID: "b", Version: 2, Score: 2}, {ID: "a", Version: 1, Score: 1}}) {
		t.Fatalf("pipeline candidates = %+v, %v", candidates, err)
	}
}

func TestPipelineSeparatesCandidateAndResultLimits(t *testing.T) {
	t.Parallel()

	candidates := make([]retrieval.Candidate, 12)
	for position := range candidates {
		candidates[position] = retrieval.Candidate{ID: string(rune('a' + position)), Version: 1}
	}

	for _, test := range []struct {
		name           string
		resultLimit    int
		candidateLimit int
		requested      int
		returned       int
	}{
		{name: "default results", resultLimit: 0, candidateLimit: 2, requested: 10, returned: 10},
		{name: "larger results", resultLimit: 12, candidateLimit: 2, requested: 12, returned: 12},
		{name: "larger candidates", resultLimit: 2, candidateLimit: 5, requested: 5, returned: 2},
	} {
		t.Run(test.name, func(t *testing.T) {
			retriever := retrieverFunc(func(_ context.Context, query retrieval.Query) ([]retrieval.Candidate, error) {
				if query.Limit != test.requested {
					t.Fatalf("candidate request limit = %d, want %d", query.Limit, test.requested)
				}

				return candidates[:min(query.Limit, len(candidates))], nil
			})
			pipeline := newPipeline(t, retrieval.WithRetrievers(retriever), retrieval.WithCandidateLimit(test.candidateLimit))

			results, err := pipeline.Retrieve(t.Context(), retrieval.Query{Text: "note", Limit: test.resultLimit})
			if err != nil || len(results) != test.returned {
				t.Fatalf("results = %+v, %v; want %d candidates", results, err, test.returned)
			}
		})
	}
}

func TestNewPipelineValidatesConfigurationWithoutRunningRetrievers(t *testing.T) {
	t.Parallel()

	retriever := retrieverFunc(func(context.Context, retrieval.Query) ([]retrieval.Candidate, error) {
		t.Fatal("invalid retrieval ran a retriever")

		return nil, nil
	})
	for _, options := range [][]retrieval.PipelineOption{
		nil,
		{retrieval.WithRetrievers(retriever, retriever)},
		{retrieval.WithRetrievers(retriever), retrieval.WithCandidateLimit(0)},
		{retrieval.WithRetrievers(retriever), retrieval.WithCandidateLimit(-1)},
	} {
		if pipeline, err := retrieval.NewPipeline(options...); pipeline != nil || err == nil {
			t.Fatalf("invalid pipeline configuration accepted: %v, %v", pipeline, err)
		}
	}

	pipeline := newPipeline(t, retrieval.WithRetrievers(retriever))
	if candidates, err := pipeline.Retrieve(t.Context(), retrieval.Query{Text: "question", Limit: -1}); candidates != nil || err == nil {
		t.Fatalf("negative result limit accepted: %+v, %v", candidates, err)
	}

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	if candidates, err := pipeline.Retrieve(ctx, retrieval.NewQuery("question")); candidates != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled retrieval = %+v, %v", candidates, err)
	}
}

func TestPipelineBypassesMergerForSingleRetriever(t *testing.T) {
	t.Parallel()

	retriever := retrieverFunc(func(context.Context, retrieval.Query) ([]retrieval.Candidate, error) {
		return []retrieval.Candidate{{ID: "note", Version: 1, Score: -2}}, nil
	})
	pipeline := newPipeline(t, retrieval.WithRetrievers(retriever), retrieval.WithMerger(mergerFunc(func(context.Context, [][]retrieval.Candidate) ([]retrieval.Candidate, error) {
		t.Fatal("single retriever invoked merger")

		return nil, nil
	})))

	candidates, err := pipeline.Retrieve(t.Context(), retrieval.NewQuery("question"))
	if err != nil || len(candidates) != 1 || candidates[0].Score != -2 {
		t.Fatalf("single-source candidates = %+v, %v", candidates, err)
	}
}

func TestPipelinePropagatesFailuresWithoutPartialResults(t *testing.T) {
	t.Parallel()

	healthy := retrieverFunc(func(context.Context, retrieval.Query) ([]retrieval.Candidate, error) {
		return []retrieval.Candidate{{ID: "note", Version: 1}}, nil
	})
	failure := errors.New("retrieval unavailable")
	broken := retrieverFunc(func(context.Context, retrieval.Query) ([]retrieval.Candidate, error) {
		return []retrieval.Candidate{{ID: "note"}}, failure
	})

	pipeline := newPipeline(t, retrieval.WithRetrievers(healthy, broken), retrieval.WithMerger(mergerFunc(func(context.Context, [][]retrieval.Candidate) ([]retrieval.Candidate, error) {
		t.Fatal("failed source invoked merger")

		return nil, nil
	})))
	if candidates, err := pipeline.Retrieve(t.Context(), retrieval.NewQuery("question")); candidates != nil || !errors.Is(err, failure) {
		t.Fatalf("failed source returned partial results: %+v, %v", candidates, err)
	}

	pipeline = newPipeline(t, retrieval.WithRetrievers(healthy, healthy), retrieval.WithMerger(mergerFunc(func(context.Context, [][]retrieval.Candidate) ([]retrieval.Candidate, error) {
		return []retrieval.Candidate{{ID: "note"}}, failure
	})))
	if candidates, err := pipeline.Retrieve(t.Context(), retrieval.NewQuery("question")); candidates != nil || !errors.Is(err, failure) {
		t.Fatalf("failed merger returned partial results: %+v, %v", candidates, err)
	}
}

type retrieverFunc func(context.Context, retrieval.Query) ([]retrieval.Candidate, error)

func (fn retrieverFunc) Retrieve(ctx context.Context, query retrieval.Query) ([]retrieval.Candidate, error) {
	return fn(ctx, query)
}

type mergerFunc func(context.Context, [][]retrieval.Candidate) ([]retrieval.Candidate, error)

func (fn mergerFunc) Merge(ctx context.Context, lists [][]retrieval.Candidate) ([]retrieval.Candidate, error) {
	return fn(ctx, lists)
}

func newPipeline(t *testing.T, options ...retrieval.PipelineOption) *retrieval.Pipeline {
	t.Helper()

	pipeline, err := retrieval.NewPipeline(options...)
	if err != nil {
		t.Fatal(err)
	}

	return pipeline
}
