package main

import (
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
)

//go:embed testdata/retrieval.json
var defaultDataset []byte

type dataset struct {
	Entries []document `json:"entries"`
	Queries []query    `json:"queries"`
}

type document struct {
	ID      string `json:"id"`
	Scope   string `json:"scope"`
	Content string `json:"content"`
}

type query struct {
	ID       string   `json:"id"`
	Text     string   `json:"text"`
	Scopes   []string `json:"scopes"`
	Relevant []string `json:"relevant"`
}

func loadDataset(data []byte) (dataset, error) {
	var corpus dataset
	if err := json.Unmarshal(data, &corpus); err != nil {
		return dataset{}, err
	}

	if len(corpus.Entries) == 0 || len(corpus.Queries) == 0 {
		return dataset{}, errors.New("dataset requires entries and queries")
	}

	documents := make(map[string]document, len(corpus.Entries))
	for _, entry := range corpus.Entries {
		if strings.TrimSpace(entry.ID) == "" || strings.TrimSpace(entry.Scope) == "" || strings.TrimSpace(entry.Content) == "" {
			return dataset{}, errors.New("dataset entries require an ID, scope, and content")
		}

		if _, exists := documents[entry.ID]; exists {
			return dataset{}, fmt.Errorf("duplicate entry %q", entry.ID)
		}

		documents[entry.ID] = entry
	}

	queries := make(map[string]bool, len(corpus.Queries))
	for _, query := range corpus.Queries {
		if queries[query.ID] {
			return dataset{}, fmt.Errorf("duplicate query %q", query.ID)
		}

		if err := validateQuery(query, documents); err != nil {
			return dataset{}, err
		}

		queries[query.ID] = true
	}

	return corpus, nil
}

func validateQuery(query query, documents map[string]document) error {
	if strings.TrimSpace(query.ID) == "" || strings.TrimSpace(query.Text) == "" || len(query.Relevant) == 0 {
		return errors.New("dataset queries require an ID, text, and relevant entries")
	}

	seen := make(map[string]bool, len(query.Relevant))
	for _, id := range query.Relevant {
		entry, exists := documents[id]
		if !exists || seen[id] {
			return fmt.Errorf("query %q has an unknown or repeated relevant entry %q", query.ID, id)
		}

		if len(query.Scopes) != 0 && !slices.Contains(query.Scopes, entry.Scope) {
			return fmt.Errorf("query %q excludes relevant entry %q by scope", query.ID, id)
		}

		seen[id] = true
	}

	return nil
}
