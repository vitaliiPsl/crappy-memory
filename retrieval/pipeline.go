package retrieval

import (
	"context"
	"errors"
	"fmt"
)

var _ Retriever = (*Pipeline)(nil)

const DefaultCandidateLimit = 100

type PipelineOption func(*Pipeline)

func WithRetrievers(retrievers ...Retriever) PipelineOption {
	return func(pipeline *Pipeline) {
		pipeline.retrievers = append(pipeline.retrievers, retrievers...)
	}
}

func WithMerger(merger Merger) PipelineOption {
	return func(pipeline *Pipeline) {
		pipeline.merger = merger
	}
}

// WithCandidateLimit sets the per-retriever budget; a larger result limit raises it.
func WithCandidateLimit(limit int) PipelineOption {
	return func(pipeline *Pipeline) {
		pipeline.candidateLimit = limit
	}
}

type Pipeline struct {
	candidateLimit int

	retrievers []Retriever
	merger     Merger
}

func NewPipeline(options ...PipelineOption) (*Pipeline, error) {
	pipeline := &Pipeline{candidateLimit: DefaultCandidateLimit}
	for _, option := range options {
		option(pipeline)
	}

	if len(pipeline.retrievers) == 0 {
		return nil, errors.New("at least one retriever is required")
	}

	if len(pipeline.retrievers) > 1 && pipeline.merger == nil {
		return nil, errors.New("multiple retrievers require a merger")
	}

	if pipeline.candidateLimit <= 0 {
		return nil, errors.New("candidate limit must be greater than zero")
	}

	return pipeline, nil
}

func (p *Pipeline) Retrieve(ctx context.Context, query Query) ([]Candidate, error) {
	if query.Limit < 0 {
		return nil, errors.New("retrieval limit cannot be negative")
	}

	if err := ctx.Err(); err != nil {
		return nil, err
	}

	limit := query.Limit
	if limit == 0 {
		limit = DefaultSearchLimit
	}

	query.Limit = max(limit, p.candidateLimit)

	lists, err := p.retrieve(ctx, query)
	if err != nil {
		return nil, err
	}

	candidates, err := p.merge(ctx, lists)
	if err != nil {
		return nil, err
	}

	return candidates[:min(limit, len(candidates))], nil
}

func (p *Pipeline) retrieve(ctx context.Context, query Query) ([][]Candidate, error) {
	lists := make([][]Candidate, len(p.retrievers))
	for position, retriever := range p.retrievers {
		if err := ctx.Err(); err != nil {
			return nil, err
		}

		candidates, err := retriever.Retrieve(ctx, query)
		if err != nil {
			return nil, fmt.Errorf("retrieve candidates from %T: %w", retriever, err)
		}

		lists[position] = candidates
	}

	return lists, nil
}

func (p *Pipeline) merge(ctx context.Context, lists [][]Candidate) ([]Candidate, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	if len(lists) == 1 {
		return lists[0], nil
	}

	candidates, err := p.merger.Merge(ctx, lists)
	if err != nil {
		return nil, fmt.Errorf("merge candidates: %w", err)
	}

	return candidates, nil
}
