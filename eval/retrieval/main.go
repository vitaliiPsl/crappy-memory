package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/vitaliipsl/crappy-memory/index/vector"
	"github.com/vitaliipsl/crappy-memory/index/vector/embedding/ollama"
)

func main() {
	if err := run(os.Stdout); err != nil {
		log.Fatal(err)
	}
}

type report struct {
	DatasetSHA256  string       `json:"dataset_sha256"`
	Entries        int          `json:"entries"`
	Queries        int          `json:"queries"`
	K              int          `json:"k"`
	Runs           int          `json:"runs"`
	Model          string       `json:"model,omitempty"`
	QueryPrefix    string       `json:"query_prefix,omitempty"`
	DocumentPrefix string       `json:"document_prefix,omitempty"`
	Evaluations    []evaluation `json:"evaluations"`
}

func run(output io.Writer) error {
	path := flag.String("dataset", "", "Dataset JSON file; defaults to the bundled corpus")
	limit := flag.Int("k", 3, "Results per query")
	runs := flag.Int("runs", 3, "Measured passes after one warm-up pass")
	model := flag.String("model", "", "Ollama embedding model; enables vector and hybrid evaluation")
	baseURL := flag.String("ollama-url", "http://localhost:11434", "Ollama base URL")
	queryPrefix := flag.String("query-prefix", "", "Prefix prepended to queries before embedding")
	documentPrefix := flag.String("document-prefix", "", "Prefix prepended to documents before embedding")
	jsonOutput := flag.Bool("json", false, "Write the complete report as JSON")

	flag.Parse()

	data := defaultDataset
	if *path != "" {
		var err error

		data, err = os.ReadFile(*path)
		if err != nil {
			return err
		}
	}

	corpus, err := loadDataset(data)
	if err != nil {
		return err
	}

	var embedder vector.Embedder
	if *model != "" {
		embedder, err = ollama.New(*model,
			ollama.WithBaseURL(*baseURL),
			ollama.WithQueryPrefix(*queryPrefix),
			ollama.WithDocumentPrefix(*documentPrefix),
		)
		if err != nil {
			return err
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	reports, err := evaluateDataset(ctx, corpus, embedder, *limit, *runs)
	if err != nil {
		return err
	}

	if *jsonOutput {
		encoder := json.NewEncoder(output)
		encoder.SetIndent("", "  ")

		return encoder.Encode(report{
			DatasetSHA256: fmt.Sprintf("%x", sha256.Sum256(data)),
			Entries:       len(corpus.Entries), Queries: len(corpus.Queries), K: *limit, Runs: *runs,
			Model: *model, QueryPrefix: *queryPrefix, DocumentPrefix: *documentPrefix, Evaluations: reports,
		})
	}

	if _, err := fmt.Fprintf(output, "%d entries, %d queries, k=%d, %d measured runs after one warm-up pass\n", len(corpus.Entries), len(corpus.Queries), *limit, *runs); err != nil {
		return err
	}

	if *model != "" {
		if _, err := fmt.Fprintf(output, "Embedding model: %s\nQuery prefix: %q\nDocument prefix: %q\n", *model, *queryPrefix, *documentPrefix); err != nil {
			return err
		}
	}

	return printReports(output, reports, *limit)
}

func printReports(output io.Writer, reports []evaluation, limit int) error {
	table := tabwriter.NewWriter(output, 0, 4, 2, ' ', 0)
	if _, err := fmt.Fprintf(table, "\nMode\tRecall@%d\tPrecision@%d\tMRR@%d\tMean ms\tP95 ms\n", limit, limit, limit); err != nil {
		return err
	}

	for _, report := range reports {
		if _, err := fmt.Fprintf(table, "%s\t%.3f\t%.3f\t%.3f\t%.3f\t%.3f\n", report.Mode, report.Recall, report.Precision, report.MRR,
			float64(report.MeanLatency)/float64(time.Millisecond), float64(report.P95Latency)/float64(time.Millisecond)); err != nil {
			return err
		}
	}

	if err := table.Flush(); err != nil {
		return err
	}

	if _, err := fmt.Fprintln(table, "\nMode\tQuery\tRecall\tPrecision\tRR\tMean ms\tRetrieved IDs"); err != nil {
		return err
	}

	for _, report := range reports {
		for _, query := range report.Queries {
			if _, err := fmt.Fprintf(table, "%s\t%s\t%.3f\t%.3f\t%.3f\t%.3f\t%s\n", report.Mode, query.ID, query.Recall, query.Precision, query.Reciprocal,
				float64(query.Latency)/float64(time.Millisecond), strings.Join(query.Retrieved, ", ")); err != nil {
				return err
			}
		}
	}

	return table.Flush()
}
