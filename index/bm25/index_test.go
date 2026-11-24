package bm25_test

import (
	"context"
	"errors"
	"iter"
	"math"
	"strings"
	"testing"

	"github.com/vitaliipsl/crappy-memory/index/bm25"
	"github.com/vitaliipsl/crappy-memory/retrieval"
)

func TestIndexRanksTermsAndReturnsVersionedCandidates(t *testing.T) {
	t.Parallel()

	ctx := t.Context()

	index := newIndex(t)
	for _, item := range []retrieval.Item{
		{ID: "a", Scope: "personal", Text: "Alpha", Version: 2},
		{ID: "b", Scope: "personal", Text: "Beta", Version: 1},
	} {
		if err := index.Upsert(ctx, item); err != nil {
			t.Fatal(err)
		}
	}

	candidates, err := index.Retrieve(ctx, retrieval.NewQuery("ALPHA alpha"))
	if err != nil {
		t.Fatal(err)
	}

	if len(candidates) != 1 || candidates[0].ID != "a" || candidates[0].Version != 2 || math.Abs(candidates[0].Score-math.Log(2)) > 1e-12 {
		t.Fatalf("candidates = %+v, want entry a with score ln(2)", candidates)
	}

	for _, item := range []retrieval.Item{
		{ID: "short", Text: "tool tool", Version: 1},
		{ID: "long", Text: "tool " + strings.Repeat("unrelated ", 20), Version: 1},
		{ID: "unicode", Text: "MÉMOIRE, 世界!", Version: 1},
	} {
		if err := index.Upsert(ctx, item); err != nil {
			t.Fatal(err)
		}
	}

	candidates, err = index.Retrieve(ctx, retrieval.NewQuery("tool"))
	if err != nil || len(candidates) != 2 || candidates[0].ID != "short" || candidates[0].Score <= candidates[1].Score {
		t.Fatalf("term frequency and length ranking = %+v, %v", candidates, err)
	}

	candidates, err = index.Retrieve(ctx, retrieval.NewQuery("mémoire 世界"))
	if err != nil || len(candidates) != 1 || candidates[0].ID != "unicode" {
		t.Fatalf("unicode query = %+v, %v", candidates, err)
	}
}

func TestIndexConfiguresSaturation(t *testing.T) {
	t.Parallel()

	idf := math.Log(1.2)
	for _, test := range []struct {
		name          string
		saturation    float64
		firstID       string
		repeatedScore float64
	}{
		{name: "disabled", saturation: 0, firstID: "a-once", repeatedScore: idf},
		{name: "custom", saturation: 2, firstID: "b-repeated", repeatedScore: idf * 30 / 11},
		{name: "large finite", saturation: math.MaxFloat64, firstID: "b-repeated", repeatedScore: idf * 20},
	} {
		t.Run(test.name, func(t *testing.T) {
			index := newIndex(t, bm25.WithSaturation(test.saturation))
			for _, item := range []retrieval.Item{
				{ID: "a-once", Text: "term " + strings.Repeat("filler ", 19), Version: 1},
				{ID: "b-repeated", Text: strings.Repeat("term ", 20), Version: 1},
			} {
				if err := index.Upsert(t.Context(), item); err != nil {
					t.Fatal(err)
				}
			}

			candidates, err := index.Retrieve(t.Context(), retrieval.NewQuery("term"))
			if err != nil || len(candidates) != 2 || candidates[0].ID != test.firstID {
				t.Fatalf("configured saturation ranking = %+v, %v", candidates, err)
			}

			for _, candidate := range candidates {
				want := idf
				if candidate.ID == "b-repeated" {
					want = test.repeatedScore
				}

				if math.IsNaN(candidate.Score) || math.Abs(candidate.Score-want) > 1e-12 {
					t.Fatalf("candidate %s score = %g, want %g", candidate.ID, candidate.Score, want)
				}
			}
		})
	}
}

func TestIndexConfiguresLengthNormalization(t *testing.T) {
	t.Parallel()

	idf := math.Log(1.2)
	for _, test := range []struct {
		name          string
		normalization float64
		firstID       string
		shortScore    float64
		longScore     float64
	}{
		{name: "disabled", normalization: 0, firstID: "a-long", shortScore: idf, longScore: idf},
		{name: "partial", normalization: 0.5, firstID: "b-short", shortScore: idf * 12 / 11, longScore: idf * 12 / 13},
		{name: "full", normalization: 1, firstID: "b-short", shortScore: idf * 6 / 5, longScore: idf * 6 / 7},
	} {
		t.Run(test.name, func(t *testing.T) {
			index := newIndex(t, bm25.WithSaturation(1), bm25.WithLengthNormalization(test.normalization))
			for _, item := range []retrieval.Item{
				{ID: "a-long", Text: "term filler filler filler", Version: 1},
				{ID: "b-short", Text: "term filler", Version: 1},
			} {
				if err := index.Upsert(t.Context(), item); err != nil {
					t.Fatal(err)
				}
			}

			candidates, err := index.Retrieve(t.Context(), retrieval.NewQuery("term"))
			if err != nil || len(candidates) != 2 || candidates[0].ID != test.firstID {
				t.Fatalf("configured length normalization ranking = %+v, %v", candidates, err)
			}

			for _, candidate := range candidates {
				want := test.longScore
				if candidate.ID == "b-short" {
					want = test.shortScore
				}

				if math.IsNaN(candidate.Score) || math.Abs(candidate.Score-want) > 1e-12 {
					t.Fatalf("candidate %s score = %g, want %g", candidate.ID, candidate.Score, want)
				}
			}
		})
	}
}

func TestNewRejectsInvalidScoringOptionsBeforeLoading(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name   string
		option bm25.Option
	}{
		{name: "negative saturation", option: bm25.WithSaturation(-1)},
		{name: "NaN saturation", option: bm25.WithSaturation(math.NaN())},
		{name: "positive infinite saturation", option: bm25.WithSaturation(math.Inf(1))},
		{name: "negative infinite saturation", option: bm25.WithSaturation(math.Inf(-1))},
		{name: "negative normalization", option: bm25.WithLengthNormalization(-0.1)},
		{name: "excessive normalization", option: bm25.WithLengthNormalization(1.1)},
		{name: "NaN normalization", option: bm25.WithLengthNormalization(math.NaN())},
		{name: "positive infinite normalization", option: bm25.WithLengthNormalization(math.Inf(1))},
		{name: "negative infinite normalization", option: bm25.WithLengthNormalization(math.Inf(-1))},
	} {
		t.Run(test.name, func(t *testing.T) {
			items := func(func(retrieval.Item, error) bool) {
				t.Fatal("invalid scoring options caused item loading")
			}
			if index, err := bm25.New(items, test.option); index != nil || err == nil {
				t.Fatalf("invalid scoring options: index=%v, error=%v", index, err)
			}
		})
	}
}

func TestIndexFiltersScopesAndIDsBeforeLimiting(t *testing.T) {
	t.Parallel()

	ctx := t.Context()

	index := newIndex(t)
	for _, item := range []retrieval.Item{
		{ID: "a", Scope: "workdir:/one/child", Text: "same", Version: 1},
		{ID: "b", Scope: "workdir:/one", Text: "same", Version: 1},
		{ID: "c", Scope: "workdir:/one", Text: "same", Version: 1},
	} {
		if err := index.Upsert(ctx, item); err != nil {
			t.Fatal(err)
		}
	}

	candidates, err := index.Retrieve(ctx, retrieval.NewQuery("same", retrieval.WithScopes("workdir:/one"), retrieval.WithLimit(1)))
	if err != nil || len(candidates) != 1 || candidates[0].ID != "b" {
		t.Fatalf("scoped result limit or tie order = %+v, %v", candidates, err)
	}

	candidates, err = index.Retrieve(ctx, retrieval.NewQuery("same", retrieval.WithScopes("workdir:/one"), retrieval.WithIDs("c")))
	if err != nil || len(candidates) != 1 || candidates[0].ID != "c" {
		t.Fatalf("ID filter = %+v, %v", candidates, err)
	}

	candidates, err = index.Retrieve(ctx, retrieval.NewQuery("", retrieval.WithLimit(1)))
	if err != nil || len(candidates) != 0 {
		t.Fatalf("empty query = %+v, %v", candidates, err)
	}

	if _, err := index.Retrieve(ctx, retrieval.NewQuery("same", retrieval.WithLimit(-1))); err == nil {
		t.Fatal("negative retrieval limit was accepted")
	}
}

func TestIndexReplacesEntriesAndRemovesIdempotently(t *testing.T) {
	t.Parallel()

	ctx := t.Context()

	index := newIndex(t)
	for _, item := range []retrieval.Item{
		{ID: "note", Scope: "personal", Text: "original", Version: 1},
		{ID: "note", Scope: "personal", Text: "original", Version: 1},
		{ID: "other", Scope: "personal", Text: "original", Version: 1},
		{ID: "note", Scope: "project", Text: "replacement", Version: 2},
	} {
		if err := index.Upsert(ctx, item); err != nil {
			t.Fatal(err)
		}
	}

	candidates, err := index.Retrieve(ctx, retrieval.NewQuery("original"))
	if err != nil || len(candidates) != 1 || candidates[0].ID != "other" {
		t.Fatalf("replacement retained old terms: %+v, %v", candidates, err)
	}

	candidates, err = index.Retrieve(ctx, retrieval.NewQuery("replacement", retrieval.WithScopes("project")))
	if err != nil || len(candidates) != 1 || candidates[0].ID != "note" || candidates[0].Version != 2 {
		t.Fatalf("replacement lost current data: %+v, %v", candidates, err)
	}

	if math.Abs(candidates[0].Score-math.Log(2)) > 1e-12 {
		t.Fatalf("repeated upsert changed corpus statistics: %+v", candidates)
	}

	for _, id := range []string{"note", "note", "missing"} {
		if err := index.Remove(ctx, id); err != nil {
			t.Fatal(err)
		}
	}

	candidates, err = index.Retrieve(ctx, retrieval.NewQuery("original replacement"))
	if err != nil || len(candidates) != 1 || candidates[0].ID != "other" {
		t.Fatalf("idempotent removal changed another entry: %+v, %v", candidates, err)
	}
}

func TestIndexCanceledOperationsPreserveState(t *testing.T) {
	t.Parallel()

	index := newIndex(t)

	item := retrieval.Item{ID: "note", Text: "original", Version: 1}
	if err := index.Upsert(t.Context(), item); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	if err := index.Upsert(ctx, retrieval.Item{ID: item.ID, Text: "changed", Version: 2}); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled upsert: %v", err)
	}

	if err := index.Remove(ctx, item.ID); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled remove: %v", err)
	}

	if _, err := index.Retrieve(ctx, retrieval.NewQuery("original")); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled retrieval: %v", err)
	}

	candidates, err := index.Retrieve(t.Context(), retrieval.NewQuery("original changed"))
	if err != nil || len(candidates) != 1 || candidates[0].Version != 1 {
		t.Fatalf("canceled operations changed the index: %+v, %v", candidates, err)
	}
}

func TestIndexIgnoresOlderVersions(t *testing.T) {
	t.Parallel()

	ctx := t.Context()

	index := newIndex(t)
	for _, item := range []retrieval.Item{
		{ID: "note", Scope: "personal", Text: "current", Version: 3},
		{ID: "note", Scope: "personal", Text: "outdated", Version: 2},
	} {
		if err := index.Upsert(ctx, item); err != nil {
			t.Fatal(err)
		}
	}

	candidates, err := index.Retrieve(ctx, retrieval.NewQuery("current"))
	if err != nil || len(candidates) != 1 || candidates[0].Version != 3 {
		t.Fatalf("older version replaced a newer one: %+v, %v", candidates, err)
	}

	candidates, err = index.Retrieve(ctx, retrieval.NewQuery("outdated"))
	if err != nil || len(candidates) != 0 {
		t.Fatalf("older version was indexed: %+v, %v", candidates, err)
	}
}

func TestNewIndexesAllItems(t *testing.T) {
	t.Parallel()

	stored := []retrieval.Item{
		{ID: "a", Scope: "personal", Text: "stored", Version: 3},
		{ID: "b", Scope: "project:demo", Text: "stored", Version: 2},
		{ID: "c", Scope: "project:demo/child", Text: "stored", Version: 7},
	}

	index, err := bm25.New(items(stored...))
	if err != nil {
		t.Fatal(err)
	}

	candidates, err := index.Retrieve(t.Context(), retrieval.NewQuery("stored"))
	if err != nil || len(candidates) != len(stored) {
		t.Fatalf("initialized candidates = %+v, %v", candidates, err)
	}

	for position, candidate := range candidates {
		if candidate.ID != stored[position].ID || candidate.Version != stored[position].Version {
			t.Fatalf("initialized candidate = %+v, want %+v", candidate, stored[position])
		}
	}

	candidates, err = index.Retrieve(t.Context(), retrieval.NewQuery("stored", retrieval.WithScopes("project:demo")))
	if err != nil || len(candidates) != 1 || candidates[0].ID != "b" {
		t.Fatalf("initialized scopes = %+v, %v", candidates, err)
	}
}

func TestNewDoesNotReturnPartialIndexOnFailure(t *testing.T) {
	t.Parallel()

	failure := errors.New("store unavailable")
	failing := func(yield func(retrieval.Item, error) bool) {
		if yield(retrieval.Item{ID: "first", Text: "stored", Version: 1}, nil) {
			yield(retrieval.Item{}, failure)
		}
	}

	if index, err := bm25.New(failing); index != nil || !errors.Is(err, failure) {
		t.Fatalf("failed initialization = %v, %v", index, err)
	}

	invalid := items(retrieval.Item{ID: "first", Text: "stored", Version: 1}, retrieval.Item{ID: "invalid", Text: "stored"})
	if index, err := bm25.New(invalid); index != nil || err == nil {
		t.Fatalf("invalid item initialization = %v, %v", index, err)
	}
}

func items(values ...retrieval.Item) iter.Seq2[retrieval.Item, error] {
	return func(yield func(retrieval.Item, error) bool) {
		for _, item := range values {
			if !yield(item, nil) {
				return
			}
		}
	}
}

func newIndex(t *testing.T, options ...bm25.Option) *bm25.Index {
	t.Helper()

	index, err := bm25.New(items(), options...)
	if err != nil {
		t.Fatal(err)
	}

	return index
}
