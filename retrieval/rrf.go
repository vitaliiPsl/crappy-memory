package retrieval

import (
	"cmp"
	"context"
	"errors"
	"slices"
	"strings"
)

var _ Merger = (*RRF)(nil)

const DefaultRankConstant = 60

type RRFOption func(*RRF)

func WithRankConstant(rankConstant int) RRFOption {
	return func(rrf *RRF) {
		rrf.rankConstant = rankConstant
	}
}

type RRF struct {
	rankConstant int
}

func NewRRF(options ...RRFOption) (*RRF, error) {
	rrf := &RRF{rankConstant: DefaultRankConstant}
	for _, option := range options {
		option(rrf)
	}

	if rrf.rankConstant < 0 {
		return nil, errors.New("rank constant cannot be negative")
	}

	return rrf, nil
}

// Merge keeps the newest version any list reported, so one lagging index doesn't make a candidate stale.
func (f *RRF) Merge(ctx context.Context, lists [][]Candidate) ([]Candidate, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	combined := make(map[string]Candidate)
	for _, list := range lists {
		seen := make(map[string]bool)

		rank := 0
		for _, candidate := range list {
			if seen[candidate.ID] {
				continue
			}

			seen[candidate.ID] = true
			rank++

			fused := combined[candidate.ID]
			fused.ID = candidate.ID
			fused.Version = max(fused.Version, candidate.Version)
			fused.Score += 1 / (float64(f.rankConstant) + float64(rank))
			combined[candidate.ID] = fused
		}
	}

	candidates := make([]Candidate, 0, len(combined))
	for _, candidate := range combined {
		candidates = append(candidates, candidate)
	}

	slices.SortFunc(candidates, func(a, b Candidate) int {
		if order := cmp.Compare(b.Score, a.Score); order != 0 {
			return order
		}

		return strings.Compare(a.ID, b.ID)
	})

	return candidates, nil
}
