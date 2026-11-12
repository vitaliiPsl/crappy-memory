package main

import (
	"encoding/json"
	"testing"
)

func TestBundledDataset(t *testing.T) {
	t.Parallel()

	corpus, err := loadDataset(defaultDataset)
	if err != nil {
		t.Fatal(err)
	}

	if len(corpus.Entries) != 100 || len(corpus.Queries) != 40 {
		t.Fatalf("unexpected corpus size: %d entries, %d queries", len(corpus.Entries), len(corpus.Queries))
	}
}

func TestLoadDatasetRejectsInvalidJudgments(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name   string
		corpus dataset
	}{
		{name: "empty"},
		{name: "duplicate documents", corpus: dataset{
			Entries: []document{{ID: "a", Scope: "personal", Content: "note"}, {ID: "a", Scope: "personal", Content: "other"}},
			Queries: []query{{ID: "q", Text: "note", Relevant: []string{"a"}}},
		}},
		{name: "blank content", corpus: dataset{
			Entries: []document{{ID: "a", Scope: "personal", Content: " "}},
			Queries: []query{{ID: "q", Text: "note", Relevant: []string{"a"}}},
		}},
		{name: "unknown relevance", corpus: dataset{
			Entries: []document{{ID: "a", Scope: "personal", Content: "note"}},
			Queries: []query{{ID: "q", Text: "note", Relevant: []string{"missing"}}},
		}},
		{name: "duplicate relevance", corpus: dataset{
			Entries: []document{{ID: "a", Scope: "personal", Content: "note"}},
			Queries: []query{{ID: "q", Text: "note", Relevant: []string{"a", "a"}}},
		}},
		{name: "scope excludes relevance", corpus: dataset{
			Entries: []document{{ID: "a", Scope: "project:atlas/child", Content: "note"}},
			Queries: []query{{ID: "q", Text: "note", Scopes: []string{"project:atlas"}, Relevant: []string{"a"}}},
		}},
		{name: "duplicate queries", corpus: dataset{
			Entries: []document{{ID: "a", Scope: "personal", Content: "note"}},
			Queries: []query{{ID: "q", Text: "note", Relevant: []string{"a"}}, {ID: "q", Text: "note", Relevant: []string{"a"}}},
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			data, err := json.Marshal(test.corpus)
			if err != nil {
				t.Fatal(err)
			}

			if _, err := loadDataset(data); err == nil {
				t.Fatal("invalid dataset accepted")
			}
		})
	}
}
