package recursive_test

import (
	"slices"
	"strings"
	"testing"

	"github.com/vitaliipsl/crappy-memory/source"
	"github.com/vitaliipsl/crappy-memory/source/chunking/recursive"
)

func TestChunkerKeepsTextThatFitsWhole(t *testing.T) {
	t.Parallel()

	assertPassages(t, chunk(t, newChunker(t), "\n\n# Title\n\nSome text.\n\n"), []source.Passage{
		{Content: "# Title\n\nSome text.", Location: "lines 3-5"},
	})
}

func TestChunkerPacksParagraphsUpToSize(t *testing.T) {
	t.Parallel()

	assertPassages(t, chunk(t, newChunker(t, recursive.WithSize(10)), "aaaa\n\nbbbb\n\ncccc"), []source.Passage{
		{Content: "aaaa", Location: "line 1"},
		{Content: "bbbb\n\ncccc", Location: "lines 3-5"},
	})
}

func TestChunkerFallsBackToSmallerSeparators(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name    string
		size    int
		content string
		want    []source.Passage
	}{
		{
			name:    "lines",
			size:    15,
			content: "line one\nline two\nline three",
			want: []source.Passage{
				{Content: "line one", Location: "line 1"},
				{Content: "line two", Location: "line 2"},
				{Content: "line three", Location: "line 3"},
			},
		},
		{
			name:    "sentences",
			size:    25,
			content: "First sentence here. Second sentence here.",
			want: []source.Passage{
				{Content: "First sentence here.", Location: "line 1"},
				{Content: "Second sentence here.", Location: "line 1"},
			},
		},
		{
			name:    "words",
			size:    12,
			content: "one two three four five six",
			want: []source.Passage{
				{Content: "one two", Location: "line 1"},
				{Content: "three four", Location: "line 1"},
				{Content: "five six", Location: "line 1"},
			},
		},
		{
			name:    "characters",
			size:    4,
			content: "abcdefghij",
			want: []source.Passage{
				{Content: "abcd", Location: "line 1"},
				{Content: "efgh", Location: "line 1"},
				{Content: "ij", Location: "line 1"},
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			assertPassages(t, chunk(t, newChunker(t, recursive.WithSize(test.size)), test.content), test.want)
		})
	}
}

func TestChunkerKeepsParagraphBoundariesAfterSplittingALongParagraph(t *testing.T) {
	t.Parallel()

	assertPassages(t, chunk(t, newChunker(t, recursive.WithSize(10)), "aaaa bbbb cccc\n\ndd"), []source.Passage{
		{Content: "aaaa bbbb", Location: "line 1"},
		{Content: "cccc", Location: "line 1"},
		{Content: "dd", Location: "line 3"},
	})
}

func TestChunkerMeasuresCharactersNotBytes(t *testing.T) {
	t.Parallel()

	assertPassages(t, chunk(t, newChunker(t, recursive.WithSize(3)), "多言語多言語"), []source.Passage{
		{Content: "多言語", Location: "line 1"},
		{Content: "多言語", Location: "line 1"},
	})
}

func TestChunkerKeepsEarlierPassagesWhenTextGrows(t *testing.T) {
	t.Parallel()

	chunker := newChunker(t, recursive.WithSize(40))

	var exchanges []string

	for turn := range 12 {
		exchanges = append(exchanges, "User: question "+strings.Repeat("x", turn)+"\nAssistant: answer")
	}

	before := chunk(t, chunker, strings.Join(exchanges[:10], "\n\n"))
	after := chunk(t, chunker, strings.Join(exchanges, "\n\n"))

	if len(before) < 2 || !slices.Equal(after[:len(before)-1], before[:len(before)-1]) {
		t.Fatalf("passages before = %q, after = %q, want earlier passages unchanged", before, after)
	}
}

func TestChunkerNormalizesLineEndings(t *testing.T) {
	t.Parallel()

	assertPassages(t, chunk(t, newChunker(t, recursive.WithSize(12)), "first line\r\nsecond line\r\n"), []source.Passage{
		{Content: "first line", Location: "line 1"},
		{Content: "second line", Location: "line 2"},
	})
}

func TestChunkerSkipsBlankText(t *testing.T) {
	t.Parallel()

	for _, content := range []string{"", "\n\n", " \t\n"} {
		if passages := chunk(t, newChunker(t), content); len(passages) != 0 {
			t.Fatalf("Chunk(%q) = %+v, want no passages", content, passages)
		}
	}
}

func TestChunkerIDReflectsSize(t *testing.T) {
	t.Parallel()

	defaults := newChunker(t)
	smaller := newChunker(t, recursive.WithSize(100))

	if defaults.ID() == smaller.ID() || defaults.ID() != newChunker(t).ID() {
		t.Fatalf("chunker IDs = %q and %q", defaults.ID(), smaller.ID())
	}
}

func TestNewRejectsNonPositiveSize(t *testing.T) {
	t.Parallel()

	if _, err := recursive.New(recursive.WithSize(0)); err == nil {
		t.Fatal("New(size 0) error = nil")
	}
}

func TestChunkerRejectsInvalidUTF8(t *testing.T) {
	t.Parallel()

	if _, err := newChunker(t).Chunk([]byte{0xff, 0xfe}); err == nil {
		t.Fatal("Chunk(invalid UTF-8) error = nil")
	}
}

func newChunker(t *testing.T, options ...recursive.Option) *recursive.Chunker {
	t.Helper()

	chunker, err := recursive.New(options...)
	if err != nil {
		t.Fatal(err)
	}

	return chunker
}

func chunk(t *testing.T, chunker *recursive.Chunker, content string) []source.Passage {
	t.Helper()

	passages, err := chunker.Chunk([]byte(content))
	if err != nil {
		t.Fatal(err)
	}

	return passages
}

func assertPassages(t *testing.T, got, want []source.Passage) {
	t.Helper()

	if !slices.Equal(got, want) {
		t.Fatalf("passages =\n%q\nwant\n%q", got, want)
	}
}
