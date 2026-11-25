package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/vitaliipsl/crappy-memory/source"
)

var _ source.Store = (*Store)(nil)

const schema = `
CREATE TABLE IF NOT EXISTS documents (
	id TEXT PRIMARY KEY,
	scope TEXT NOT NULL,
	media_type TEXT NOT NULL,
	content BLOB NOT NULL,
	hash TEXT NOT NULL,
	chunker TEXT NOT NULL,
	created_at TEXT NOT NULL,
	updated_at TEXT NOT NULL
) STRICT;
CREATE INDEX IF NOT EXISTS documents_scope_id ON documents(scope, id);
CREATE TABLE IF NOT EXISTS chunks (
	id TEXT PRIMARY KEY,
	document_id TEXT NOT NULL,
	scope TEXT NOT NULL,
	position INTEGER NOT NULL,
	location TEXT NOT NULL,
	content TEXT NOT NULL
) STRICT;
CREATE INDEX IF NOT EXISTS chunks_document_id ON chunks(document_id, id);
`

const (
	documentColumns = "id, scope, media_type, content, hash, chunker, created_at, updated_at"
	chunkColumns    = "id, document_id, scope, position, location, content"

	getQuery = "SELECT " + documentColumns + " FROM documents WHERE id = ?"

	putDocumentQuery = `
INSERT INTO documents (` + documentColumns + `)
VALUES (?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(id) DO UPDATE SET
	scope = excluded.scope,
	media_type = excluded.media_type,
	content = excluded.content,
	hash = excluded.hash,
	chunker = excluded.chunker,
	created_at = excluded.created_at,
	updated_at = excluded.updated_at
`
	putChunkQuery       = "INSERT INTO chunks (" + chunkColumns + ") VALUES (?, ?, ?, ?, ?, ?)"
	deleteChunksQuery   = "DELETE FROM chunks WHERE document_id = ?"
	deleteDocumentQuery = "DELETE FROM documents WHERE id = ?"
	listQuery           = "SELECT " + documentColumns + " FROM documents WHERE id > ?%s ORDER BY id LIMIT ?"
	chunksQuery         = "SELECT " + chunkColumns + " FROM chunks WHERE id > ?%s ORDER BY id LIMIT ?"
)

type Store struct {
	db *sql.DB
}

func New(db *sql.DB) (*Store, error) {
	if _, err := db.Exec(schema); err != nil {
		return nil, fmt.Errorf("initialize source database: %w", err)
	}

	return &Store{db: db}, nil
}

func (s *Store) Get(ctx context.Context, id string) (source.Document, error) {
	document, err := scanDocument(s.db.QueryRowContext(ctx, getQuery, id))
	if errors.Is(err, sql.ErrNoRows) {
		return source.Document{}, source.ErrNotFound
	}

	if err != nil {
		return source.Document{}, fmt.Errorf("get source document: %w", err)
	}

	return document, nil
}

func (s *Store) List(ctx context.Context, filter source.Filter) (source.Page, error) {
	limit, err := pageLimit(filter.Limit)
	if err != nil {
		return source.Page{}, err
	}

	scopes, scopeArguments := in("scope", filter.Scopes)
	ids, idArguments := in("id", filter.IDs)

	arguments := append([]any{filter.AfterID}, scopeArguments...)
	arguments = append(arguments, idArguments...)
	arguments = append(arguments, limit+1)

	rows, err := s.db.QueryContext(ctx, fmt.Sprintf(listQuery, scopes+ids), arguments...)
	if err != nil {
		return source.Page{}, fmt.Errorf("list source documents: %w", err)
	}

	defer func() {
		_ = rows.Close()
	}()

	documents := make([]source.Document, 0)
	for rows.Next() {
		document, err := scanDocument(rows)
		if err != nil {
			return source.Page{}, fmt.Errorf("read source document: %w", err)
		}

		documents = append(documents, document)
	}

	if err := rows.Err(); err != nil {
		return source.Page{}, fmt.Errorf("list source documents: %w", err)
	}

	page := source.Page{Documents: documents}
	if len(documents) > limit {
		page.Documents = documents[:limit]
		page.NextID = documents[limit-1].ID
	}

	return page, nil
}

func (s *Store) Put(ctx context.Context, document source.Document, chunks []source.Chunk) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin source document write: %w", err)
	}

	defer func() {
		_ = tx.Rollback()
	}()

	// A nil slice would be written as NULL.
	content := append([]byte{}, document.Content...)

	if _, err := tx.ExecContext(ctx, putDocumentQuery,
		document.ID,
		document.Scope,
		document.MediaType,
		content,
		document.Hash,
		document.Chunker,
		document.CreatedAt.Format(time.RFC3339Nano),
		document.UpdatedAt.Format(time.RFC3339Nano),
	); err != nil {
		return fmt.Errorf("write source document: %w", err)
	}

	if _, err := tx.ExecContext(ctx, deleteChunksQuery, document.ID); err != nil {
		return fmt.Errorf("remove previous chunks: %w", err)
	}

	statement, err := tx.PrepareContext(ctx, putChunkQuery)
	if err != nil {
		return fmt.Errorf("prepare chunk write: %w", err)
	}

	defer func() {
		_ = statement.Close()
	}()

	for _, chunk := range chunks {
		if _, err := statement.ExecContext(ctx,
			chunk.ID,
			chunk.DocumentID,
			chunk.Scope,
			chunk.Position,
			chunk.Location,
			chunk.Content,
		); err != nil {
			return fmt.Errorf("write chunk %q: %w", chunk.ID, err)
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit source document write: %w", err)
	}

	return nil
}

func (s *Store) Delete(ctx context.Context, id string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin source document deletion: %w", err)
	}

	defer func() {
		_ = tx.Rollback()
	}()

	if _, err := tx.ExecContext(ctx, deleteChunksQuery, id); err != nil {
		return fmt.Errorf("delete chunks: %w", err)
	}

	result, err := tx.ExecContext(ctx, deleteDocumentQuery, id)
	if err != nil {
		return fmt.Errorf("delete source document: %w", err)
	}

	count, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("count deleted source documents: %w", err)
	}

	if count == 0 {
		return source.ErrNotFound
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit source document deletion: %w", err)
	}

	return nil
}

func (s *Store) Chunks(ctx context.Context, filter source.ChunkFilter) (source.ChunkPage, error) {
	limit, err := pageLimit(filter.Limit)
	if err != nil {
		return source.ChunkPage{}, err
	}

	documents, documentArguments := in("document_id", filter.DocumentIDs)
	ids, idArguments := in("id", filter.IDs)

	arguments := append([]any{filter.AfterID}, documentArguments...)
	arguments = append(arguments, idArguments...)
	arguments = append(arguments, limit+1)

	rows, err := s.db.QueryContext(ctx, fmt.Sprintf(chunksQuery, documents+ids), arguments...)
	if err != nil {
		return source.ChunkPage{}, fmt.Errorf("list chunks: %w", err)
	}

	defer func() {
		_ = rows.Close()
	}()

	chunks := make([]source.Chunk, 0)
	for rows.Next() {
		var chunk source.Chunk
		if err := rows.Scan(&chunk.ID, &chunk.DocumentID, &chunk.Scope, &chunk.Position, &chunk.Location, &chunk.Content); err != nil {
			return source.ChunkPage{}, fmt.Errorf("read chunk: %w", err)
		}

		chunks = append(chunks, chunk)
	}

	if err := rows.Err(); err != nil {
		return source.ChunkPage{}, fmt.Errorf("list chunks: %w", err)
	}

	page := source.ChunkPage{Chunks: chunks}
	if len(chunks) > limit {
		page.Chunks = chunks[:limit]
		page.NextID = chunks[limit-1].ID
	}

	return page, nil
}

func pageLimit(limit int) (int, error) {
	if limit < 0 {
		return 0, errors.New("source list limit cannot be negative")
	}

	if limit == 0 {
		return source.DefaultPageSize, nil
	}

	return limit, nil
}

func in(column string, values []string) (string, []any) {
	if len(values) == 0 {
		return "", nil
	}

	arguments := make([]any, len(values))
	for position, value := range values {
		arguments[position] = value
	}

	return " AND " + column + " IN (" + strings.TrimSuffix(strings.Repeat("?,", len(values)), ",") + ")", arguments
}

func scanDocument(row interface{ Scan(...any) error }) (source.Document, error) {
	var (
		document             source.Document
		createdAt, updatedAt string
	)
	if err := row.Scan(
		&document.ID,
		&document.Scope,
		&document.MediaType,
		&document.Content,
		&document.Hash,
		&document.Chunker,
		&createdAt,
		&updatedAt,
	); err != nil {
		return source.Document{}, err
	}

	var err error

	document.CreatedAt, err = time.Parse(time.RFC3339Nano, createdAt)
	if err != nil {
		return source.Document{}, fmt.Errorf("parse source document creation time: %w", err)
	}

	document.UpdatedAt, err = time.Parse(time.RFC3339Nano, updatedAt)
	if err != nil {
		return source.Document{}, fmt.Errorf("parse source document update time: %w", err)
	}

	return document, nil
}
