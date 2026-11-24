package retrieval_test

import (
	"context"
	"errors"
	"math"
	"testing"

	"github.com/vitaliipsl/crappy-memory/retrieval"
)

func TestRRFCombinesRankings(t *testing.T) {
	t.Parallel()

	fusion, err := retrieval.NewRRF()
	if err != nil {
		t.Fatal(err)
	}

	lists := [][]retrieval.Candidate{
		{{ID: "a", Version: 3, Score: 1000}, {ID: "a", Version: 3, Score: 900}, {ID: "b", Version: 2, Score: 0.01}, {ID: "d", Version: 1}},
		{{ID: "b", Version: 2, Score: -100}, {ID: "c", Version: 1, Score: 0.9}, {ID: "a", Version: 3, Score: -300}},
	}

	candidates, err := fusion.Merge(t.Context(), lists)
	if err != nil || len(candidates) != 4 {
		t.Fatalf("fused candidates = %+v, %v", candidates, err)
	}

	want := []retrieval.Candidate{
		{ID: "b", Version: 2, Score: 1.0/62 + 1.0/61},
		{ID: "a", Version: 3, Score: 1.0/61 + 1.0/63},
		{ID: "c", Version: 1, Score: 1.0 / 62},
		{ID: "d", Version: 1, Score: 1.0 / 63},
	}
	for position, expected := range want {
		got := candidates[position]
		if got.ID != expected.ID || got.Version != expected.Version || math.Abs(got.Score-expected.Score) > 1e-12 {
			t.Fatalf("fused position %d = %+v, want %+v", position, got, expected)
		}
	}

	if lists[0][0].Score != 1000 || lists[1][2].Score != -300 {
		t.Fatalf("fusion mutated input lists: %+v", lists)
	}
}

func TestRRFKeepsNewestReportedVersion(t *testing.T) {
	t.Parallel()

	fusion, err := retrieval.NewRRF()
	if err != nil {
		t.Fatal(err)
	}

	candidates, err := fusion.Merge(t.Context(), [][]retrieval.Candidate{
		{{ID: "note", Version: 1}},
		{{ID: "note", Version: 3}},
		{{ID: "note", Version: 2}},
	})
	if err != nil || len(candidates) != 1 || candidates[0].Version != 3 {
		t.Fatalf("fused version = %+v, %v", candidates, err)
	}
}

func TestRRFBreaksTiesByID(t *testing.T) {
	t.Parallel()

	fusion, err := retrieval.NewRRF(retrieval.WithRankConstant(0))
	if err != nil {
		t.Fatal(err)
	}

	candidates, err := fusion.Merge(t.Context(), [][]retrieval.Candidate{
		{{ID: "b", Version: 1}},
		{{ID: "a", Version: 1}},
	})
	if err != nil || len(candidates) != 2 || candidates[0].ID != "a" || candidates[1].ID != "b" || candidates[0].Score != 1 || candidates[1].Score != 1 {
		t.Fatalf("tied candidates = %+v, %v", candidates, err)
	}
}

func TestRRFValidatesConfigurationAndCancellation(t *testing.T) {
	t.Parallel()

	if fusion, err := retrieval.NewRRF(retrieval.WithRankConstant(-1)); fusion != nil || err == nil {
		t.Fatalf("negative rank constant accepted: %v, %v", fusion, err)
	}

	fusion, err := retrieval.NewRRF()
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	if candidates, err := fusion.Merge(ctx, nil); candidates != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled fusion = %+v, %v", candidates, err)
	}
}
