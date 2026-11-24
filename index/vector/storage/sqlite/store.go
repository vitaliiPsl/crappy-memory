package sqlite

import (
	"cmp"
	"context"
	"database/sql"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/vitaliipsl/crappy-memory/index/vector"
	"github.com/vitaliipsl/crappy-memory/retrieval"
)

var _ vector.Store = (*Store)(nil)

const DefaultTable = "vectors"

const schema = `
CREATE TABLE IF NOT EXISTS "%[1]s" (
	id TEXT PRIMARY KEY,
	scope TEXT NOT NULL,
	version TEXT NOT NULL,
	space TEXT NOT NULL,
	embedding BLOB NOT NULL
) STRICT;
CREATE INDEX IF NOT EXISTS "%[1]s_space_scope" ON "%[1]s"(space, scope);
`

const (
	columns = "id, scope, version, space, embedding"

	getQuery    = `SELECT ` + columns + ` FROM "%s" WHERE id = ?`
	listQuery   = `SELECT ` + columns + ` FROM "%s" WHERE id > ? ORDER BY id`
	searchQuery = `SELECT ` + columns + ` FROM "%s" WHERE space = ?%s`

	upsertQuery = `
INSERT INTO "%s" (id, scope, version, space, embedding)
VALUES (?, ?, ?, ?, ?)
ON CONFLICT(id) DO UPDATE SET
	scope = excluded.scope,
	version = excluded.version,
	space = excluded.space,
	embedding = excluded.embedding
`

	removeQuery = `DELETE FROM "%s" WHERE id = ?`
)

var tableName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

type Option func(*Store)

func WithSimilarity(similarity vector.Similarity) Option {
	return func(store *Store) {
		store.similarity = similarity
	}
}

func WithTable(table string) Option {
	return func(store *Store) {
		store.table = table
	}
}

type Store struct {
	db         *sql.DB
	table      string
	similarity vector.Similarity
}

func New(db *sql.DB, options ...Option) (*Store, error) {
	store := &Store{db: db, table: DefaultTable, similarity: vector.Cosine}
	for _, option := range options {
		option(store)
	}

	if !tableName.MatchString(store.table) {
		return nil, fmt.Errorf("vector table name must be a plain SQL identifier: %q", store.table)
	}

	if _, err := db.Exec(fmt.Sprintf(schema, store.table)); err != nil {
		return nil, fmt.Errorf("initialize vector database: %w", err)
	}

	return store, nil
}

func (s *Store) Get(ctx context.Context, id string) (vector.Record, error) {
	row := s.db.QueryRowContext(ctx, fmt.Sprintf(getQuery, s.table), id)

	record, err := scanRecord(row)
	if errors.Is(err, sql.ErrNoRows) {
		return vector.Record{}, vector.ErrNotFound
	}

	if err != nil {
		return vector.Record{}, fmt.Errorf("load vector %q: %w", id, err)
	}

	return record, nil
}

func (s *Store) List(ctx context.Context, afterID string, limit int) (vector.Page, error) {
	if limit <= 0 {
		return vector.Page{}, errors.New("vector list limit must be positive")
	}

	rows, err := s.db.QueryContext(ctx, fmt.Sprintf(listQuery, s.table), afterID)
	if err != nil {
		return vector.Page{}, fmt.Errorf("list vectors: %w", err)
	}

	defer func() { _ = rows.Close() }()

	page := vector.Page{Records: make([]vector.Record, 0)}
	for rows.Next() {
		if len(page.Records) == limit {
			page.NextID = page.Records[len(page.Records)-1].ID

			break
		}

		record, err := scanRecord(rows)
		if err != nil {
			return vector.Page{}, fmt.Errorf("read vector page: %w", err)
		}

		page.Records = append(page.Records, record)
	}

	if err := rows.Err(); err != nil {
		return vector.Page{}, fmt.Errorf("list vectors: %w", err)
	}

	return page, nil
}

func (s *Store) Upsert(ctx context.Context, records []vector.Record) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin vector upsert: %w", err)
	}

	defer func() { _ = tx.Rollback() }()

	statement, err := tx.PrepareContext(ctx, fmt.Sprintf(upsertQuery, s.table))
	if err != nil {
		return fmt.Errorf("prepare vector upsert: %w", err)
	}

	defer func() { _ = statement.Close() }()

	for _, record := range records {
		if record.ID == "" || record.Version == 0 {
			return errors.New("vector record ID and version are required")
		}

		if strings.TrimSpace(record.Space) == "" {
			return errors.New("vector record space is required")
		}

		if err := validateVector(record.Values); err != nil {
			return fmt.Errorf("vector %q: %w", record.ID, err)
		}

		if _, err := statement.ExecContext(ctx, record.ID, record.Scope, strconv.FormatUint(record.Version, 10), record.Space, encodeVector(record.Values)); err != nil {
			return fmt.Errorf("upsert vector %q: %w", record.ID, err)
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit vector upsert: %w", err)
	}

	return nil
}

func (s *Store) Remove(ctx context.Context, id string) error {
	if id == "" {
		return errors.New("removed vector ID is required")
	}

	if _, err := s.db.ExecContext(ctx, fmt.Sprintf(removeQuery, s.table), id); err != nil {
		return fmt.Errorf("remove vector %q: %w", id, err)
	}

	return nil
}

func (s *Store) Search(ctx context.Context, query vector.Query) ([]retrieval.Candidate, error) {
	if strings.TrimSpace(query.Space) == "" {
		return nil, errors.New("query space is required")
	}

	if query.Limit < 0 {
		return nil, errors.New("vector search limit cannot be negative")
	}

	if err := validateVector(query.Values); err != nil {
		return nil, fmt.Errorf("query vector: %w", err)
	}

	limit := query.Limit
	if limit == 0 {
		limit = retrieval.DefaultSearchLimit
	}

	where, arguments := filters(query.Scopes, query.IDs)
	arguments = append([]any{query.Space}, arguments...)

	rows, err := s.db.QueryContext(ctx, fmt.Sprintf(searchQuery, s.table, where), arguments...)
	if err != nil {
		return nil, fmt.Errorf("search vectors: %w", err)
	}

	defer func() { _ = rows.Close() }()

	candidates := make([]retrieval.Candidate, 0)
	for rows.Next() {
		record, err := scanRecord(rows)
		if err != nil {
			return nil, fmt.Errorf("read search vector: %w", err)
		}

		if len(query.Values) != len(record.Values) {
			return nil, fmt.Errorf("vector %q has %d dimensions, query has %d", record.ID, len(record.Values), len(query.Values))
		}

		score := s.similarity(query.Values, record.Values)
		if math.IsNaN(score) || math.IsInf(score, 0) {
			return nil, fmt.Errorf("vector %q similarity score must be finite", record.ID)
		}

		candidates = append(candidates, retrieval.Candidate{ID: record.ID, Version: record.Version, Score: score})
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("search vectors: %w", err)
	}

	slices.SortFunc(candidates, func(a, b retrieval.Candidate) int {
		if order := cmp.Compare(b.Score, a.Score); order != 0 {
			return order
		}

		return strings.Compare(a.ID, b.ID)
	})

	return candidates[:min(limit, len(candidates))], nil
}

func filters(scopes, ids []string) (string, []any) {
	var (
		where     string
		arguments []any
	)

	if len(scopes) > 0 {
		where += " AND scope IN (" + strings.TrimSuffix(strings.Repeat("?,", len(scopes)), ",") + ")"
		for _, scope := range scopes {
			arguments = append(arguments, scope)
		}
	}

	if len(ids) > 0 {
		where += " AND id IN (" + strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",") + ")"
		for _, id := range ids {
			arguments = append(arguments, id)
		}
	}

	return where, arguments
}

func scanRecord(row interface{ Scan(...any) error }) (vector.Record, error) {
	var (
		record vector.Record
		values []byte
	)

	if err := row.Scan(&record.ID, &record.Scope, &record.Version, &record.Space, &values); err != nil {
		return vector.Record{}, err
	}

	if len(values) == 0 || len(values)%4 != 0 {
		return vector.Record{}, errors.New("stored vector must contain float32 values")
	}

	record.Values = make([]float32, len(values)/4)
	for position := range record.Values {
		record.Values[position] = math.Float32frombits(binary.LittleEndian.Uint32(values[position*4:]))
	}

	if err := validateVector(record.Values); err != nil {
		return vector.Record{}, err
	}

	return record, nil
}

func encodeVector(values []float32) []byte {
	data := make([]byte, 0, len(values)*4)
	for _, value := range values {
		data = binary.LittleEndian.AppendUint32(data, math.Float32bits(value))
	}

	return data
}

func validateVector(values []float32) error {
	var squaredNorm float64
	for _, value := range values {
		coordinate := float64(value)
		if math.IsNaN(coordinate) || math.IsInf(coordinate, 0) {
			return errors.New("vector coordinates must be finite")
		}

		squaredNorm += coordinate * coordinate
	}

	if squaredNorm == 0 {
		return errors.New("vector must be non-zero")
	}

	return nil
}
