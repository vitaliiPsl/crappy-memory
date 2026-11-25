package recursive

import (
	"errors"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/vitaliipsl/crappy-memory/source"
)

var _ source.Chunker = (*Chunker)(nil)

const DefaultSize = 2000

var separators = []string{"\n\n", "\n", ". ", " "}

type Option func(*Chunker)

func WithSize(size int) Option {
	return func(chunker *Chunker) {
		chunker.size = size
	}
}

type Chunker struct {
	size int
}

type span struct {
	start, end int
	size       int
}

func New(options ...Option) (*Chunker, error) {
	chunker := &Chunker{
		size: DefaultSize,
	}
	for _, option := range options {
		option(chunker)
	}

	if chunker.size <= 0 {
		return nil, errors.New("chunk size must be positive")
	}

	return chunker, nil
}

func (c *Chunker) ID() string {
	return fmt.Sprintf("recursive/v1 size=%d", c.size)
}

func (c *Chunker) Chunk(content []byte) ([]source.Passage, error) {
	if !utf8.Valid(content) {
		return nil, errors.New("chunked content must be valid UTF-8")
	}

	text := strings.ReplaceAll(string(content), "\r\n", "\n")

	line, counted := 1, 0
	lineAt := func(offset int) int {
		line += strings.Count(text[counted:offset], "\n")
		counted = offset

		return line
	}

	passages := make([]source.Passage, 0)
	for _, piece := range c.split(text, span{end: len(text), size: utf8.RuneCountInString(text)}, 0) {
		raw := text[piece.start:piece.end]

		trimmed := strings.TrimSpace(raw)
		if trimmed == "" {
			continue
		}

		start := piece.start + len(raw) - len(strings.TrimLeftFunc(raw, unicode.IsSpace))
		passages = append(passages, source.Passage{
			Content:  trimmed,
			Location: location(lineAt(start), lineAt(start+len(trimmed)-1)),
		})
	}

	return passages, nil
}

func (c *Chunker) split(text string, whole span, level int) []span {
	if whole.size <= c.size {
		return []span{whole}
	}

	if level == len(separators) {
		return c.cut(text, whole)
	}

	var chunks, pending []span

	start := whole.start
	for _, part := range strings.SplitAfter(text[whole.start:whole.end], separators[level]) {
		if part == "" {
			continue
		}

		piece := span{start: start, end: start + len(part), size: utf8.RuneCountInString(part)}
		start = piece.end

		if piece.size <= c.size {
			pending = append(pending, piece)

			continue
		}

		chunks = append(chunks, c.merge(pending)...)
		chunks = append(chunks, c.split(text, piece, level+1)...)
		pending = nil
	}

	return append(chunks, c.merge(pending)...)
}

func (c *Chunker) merge(pieces []span) []span {
	var merged []span

	for _, piece := range pieces {
		if last := len(merged) - 1; last >= 0 && merged[last].size+piece.size <= c.size {
			merged[last].end = piece.end
			merged[last].size += piece.size

			continue
		}

		merged = append(merged, piece)
	}

	return merged
}

func (c *Chunker) cut(text string, whole span) []span {
	var pieces []span

	current := span{start: whole.start}
	for offset := range text[whole.start:whole.end] {
		if current.size == c.size {
			current.end = whole.start + offset
			pieces = append(pieces, current)
			current = span{start: current.end}
		}

		current.size++
	}

	current.end = whole.end

	return append(pieces, current)
}

func location(start, end int) string {
	if start == end {
		return fmt.Sprintf("line %d", start)
	}

	return fmt.Sprintf("lines %d-%d", start, end)
}
