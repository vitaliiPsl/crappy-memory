package source_test

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/vitaliipsl/crappy-memory/retrieval"
	"github.com/vitaliipsl/crappy-memory/source"
)

const mediaType = "text/plain"

func TestSourcesPutStoresAndIndexesChunks(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	store := newFakeStore()
	index := newRecordingIndex()
	sources := newSources(t, store, source.WithIndexer(index, failOnIndexError(t)))

	document, err := sources.Put(ctx, "notes", "project", mediaType, []byte("alpha\n\nbeta"))
	if err != nil {
		t.Fatal(err)
	}

	if document.ID != "notes" || document.Scope != "project" || document.MediaType != mediaType || string(document.Content) != "alpha\n\nbeta" {
		t.Fatalf("document = %+v", document)
	}

	if document.Hash == "" || document.Chunker != "paragraphs/v1" || document.CreatedAt.Location() != time.UTC || document.UpdatedAt != document.CreatedAt {
		t.Fatalf("document bookkeeping = %+v", document)
	}

	stored, err := sources.Get(ctx, "notes")
	if err != nil || stored.Hash != document.Hash {
		t.Fatalf("Get() = %+v, %v", stored, err)
	}

	chunks := store.documentChunks("notes")
	if len(chunks) != 2 {
		t.Fatalf("stored chunks = %+v", chunks)
	}

	for position, content := range []string{"alpha", "beta"} {
		chunk := chunks[position]
		if chunk.ID == "" || chunk.DocumentID != "notes" || chunk.Scope != "project" || chunk.Position != position || chunk.Content != content || chunk.Location != fmt.Sprintf("part %d", position+1) {
			t.Fatalf("chunk %d = %+v", position, chunk)
		}

		want := retrieval.Item{ID: chunk.ID, Scope: "project", Version: 1, Text: content}
		if got := index.item(chunk.ID); got != want {
			t.Fatalf("indexed item = %+v, want %+v", got, want)
		}
	}
}

func TestSourcesPutOfUnchangedDocumentWritesNothing(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	store := newFakeStore()
	index := newRecordingIndex()
	sources := newSources(t, store, source.WithIndexer(index, failOnIndexError(t)))

	first, err := sources.Put(ctx, "notes", "project", mediaType, []byte("alpha\n\nbeta"))
	if err != nil {
		t.Fatal(err)
	}

	second, err := sources.Put(ctx, "notes", "project", mediaType, []byte("alpha\n\nbeta"))
	if err != nil {
		t.Fatal(err)
	}

	if store.putCount() != 1 || len(index.upserted()) != 2 || second.UpdatedAt != first.UpdatedAt {
		t.Fatalf("puts = %d, upserts = %v, second = %+v", store.putCount(), index.upserted(), second)
	}
}

func TestSourcesPutReindexesOnlyChangedPassages(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	store := newFakeStore()
	index := newRecordingIndex()
	sources := newSources(t, store, source.WithIndexer(index, failOnIndexError(t)))

	original, err := sources.Put(ctx, "notes", "project", mediaType, []byte("alpha\n\nbeta"))
	if err != nil {
		t.Fatal(err)
	}

	before := chunkIDs(store.documentChunks("notes"))

	edited, err := sources.Put(ctx, "notes", "project", mediaType, []byte("alpha\n\ngamma\n\nbeta"))
	if err != nil {
		t.Fatal(err)
	}

	after := store.documentChunks("notes")
	if after[0].ID != before[0] || after[2].ID != before[1] || after[2].Position != 2 || after[2].Location != "part 3" {
		t.Fatalf("chunks after inserting a passage = %+v, want unchanged passages to keep their IDs", after)
	}

	if upserts := index.upserted(); len(upserts) != 3 || upserts[2] != after[1].ID || len(index.removed()) != 0 {
		t.Fatalf("upserts = %v, removes = %v, want only the inserted passage indexed", upserts, index.removed())
	}

	if edited.CreatedAt != original.CreatedAt || edited.UpdatedAt.Before(original.UpdatedAt) {
		t.Fatalf("edited timestamps = %+v, original = %+v", edited, original)
	}

	if _, err := sources.Put(ctx, "notes", "project", mediaType, []byte("alpha")); err != nil {
		t.Fatal(err)
	}

	if removes := index.removed(); !sameIDs(removes, []string{after[1].ID, after[2].ID}) || !sameIDs(index.ids(), []string{before[0]}) {
		t.Fatalf("removes = %v, indexed = %v, want dropped passages removed", removes, index.ids())
	}
}

func TestSourcesChunkIDsDependOnDocumentScopeAndRepetition(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	store := newFakeStore()
	index := newRecordingIndex()
	sources := newSources(t, store, source.WithIndexer(index, failOnIndexError(t)))

	for _, id := range []string{"first", "second"} {
		if _, err := sources.Put(ctx, id, "project", mediaType, []byte("same\n\nsame")); err != nil {
			t.Fatal(err)
		}
	}

	first, second := chunkIDs(store.documentChunks("first")), chunkIDs(store.documentChunks("second"))
	if first[0] == first[1] || slices.Contains(second, first[0]) || slices.Contains(second, first[1]) {
		t.Fatalf("chunk IDs = %v and %v, want repeats and documents to differ", first, second)
	}

	if _, err := sources.Put(ctx, "first", "personal", mediaType, []byte("same\n\nsame")); err != nil {
		t.Fatal(err)
	}

	moved := store.documentChunks("first")
	if slices.Contains(first, moved[0].ID) || index.item(moved[0].ID).Scope != "personal" || slices.ContainsFunc(first, index.contains) {
		t.Fatalf("chunks after a scope change = %+v, want new IDs indexed in the new scope", moved)
	}
}

func TestSourcesPutRejectsInvalidDocumentsWithoutWriting(t *testing.T) {
	t.Parallel()

	failure := errors.New("unreadable")
	store := newFakeStore()
	index := newRecordingIndex()
	sources := newSources(t, store,
		source.WithChunker("broken", paragraphChunker{id: "broken", err: failure}),
		source.WithIndexer(index, failOnIndexError(t)),
	)

	for _, test := range []struct {
		id, scope, mediaType string
	}{
		{id: " ", scope: "project", mediaType: mediaType},
		{id: "notes", scope: "", mediaType: mediaType},
		{id: "notes", scope: "project", mediaType: ""},
		{id: "notes", scope: "project", mediaType: "application/pdf"},
		{id: "notes", scope: "project", mediaType: "broken"},
	} {
		if _, err := sources.Put(t.Context(), test.id, test.scope, test.mediaType, []byte("alpha")); err == nil {
			t.Fatalf("Put(%+v) error = nil", test)
		}
	}

	if _, err := sources.Put(t.Context(), "notes", "project", "broken", []byte("alpha")); !errors.Is(err, failure) {
		t.Fatalf("Put(chunker failure) error = %v, want %v", err, failure)
	}

	if store.putCount() != 0 || len(index.upserted()) != 0 {
		t.Fatalf("puts = %d, upserts = %v, want nothing written", store.putCount(), index.upserted())
	}
}

func TestSourcesPutReportsIndexErrorsWithoutFailing(t *testing.T) {
	t.Parallel()

	failure := errors.New("index unavailable")
	index := newRecordingIndex()
	index.fail(failure)

	var reported []error

	store := newFakeStore()
	sources := newSources(t, store, source.WithIndexer(index, func(_ context.Context, err error) {
		reported = append(reported, err)
	}))

	if _, err := sources.Put(t.Context(), "notes", "project", mediaType, []byte("alpha\n\nbeta")); err != nil {
		t.Fatal(err)
	}

	if len(reported) != 2 || !errors.Is(reported[0], failure) || len(store.documentChunks("notes")) != 2 {
		t.Fatalf("reported = %v, stored = %+v", reported, store.documentChunks("notes"))
	}
}

func TestSourcesDeleteRemovesChunksFromIndexes(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	index := newRecordingIndex()
	sources := newSources(t, newFakeStore(), source.WithIndexer(index, failOnIndexError(t)))

	for _, id := range []string{"deleted", "kept"} {
		if _, err := sources.Put(ctx, id, "project", mediaType, []byte(id+"\n\npassage")); err != nil {
			t.Fatal(err)
		}
	}

	if err := sources.Delete(ctx, "deleted"); err != nil {
		t.Fatal(err)
	}

	if _, err := sources.Get(ctx, "deleted"); !errors.Is(err, source.ErrNotFound) {
		t.Fatalf("Get(deleted) error = %v, want ErrNotFound", err)
	}

	if len(index.ids()) != 2 || len(index.removed()) != 2 {
		t.Fatalf("indexed = %v, removed = %v, want only the kept document indexed", index.ids(), index.removed())
	}

	if err := sources.Delete(ctx, "deleted"); !errors.Is(err, source.ErrNotFound) {
		t.Fatalf("Delete(deleted) error = %v, want ErrNotFound", err)
	}
}

func TestSourcesListsDocuments(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	sources := newSources(t, newFakeStore())

	for _, id := range []string{"b", "a"} {
		if _, err := sources.Put(ctx, id, "project", mediaType, []byte(id)); err != nil {
			t.Fatal(err)
		}
	}

	page, err := sources.List(ctx, source.Filter{})
	if err != nil || len(page.Documents) != 2 || page.Documents[0].ID != "a" {
		t.Fatalf("List() = %+v, %v", page, err)
	}
}

func TestSourcesSearchRequiresRetriever(t *testing.T) {
	t.Parallel()

	if _, err := newSources(t, newFakeStore()).Search(t.Context(), "alpha"); !errors.Is(err, source.ErrRetrieverRequired) {
		t.Fatalf("Search() error = %v, want ErrRetrieverRequired", err)
	}
}

func TestSourcesSearchResolvesCandidatesWithOneStoreRead(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	store := newFakeStore()

	var candidates []retrieval.Candidate

	retriever := retrieverFunc(func(context.Context, retrieval.Query) ([]retrieval.Candidate, error) {
		return candidates, nil
	})
	sources := newSources(t, store, source.WithRetriever(retriever))

	for id, scope := range map[string]string{"project-notes": "project", "personal-notes": "personal"} {
		if _, err := sources.Put(ctx, id, scope, mediaType, []byte(id)); err != nil {
			t.Fatal(err)
		}
	}

	project, personal := store.documentChunks("project-notes")[0], store.documentChunks("personal-notes")[0]
	candidates = []retrieval.Candidate{
		{ID: project.ID, Version: 1, Score: 3},
		{ID: "deleted", Version: 1, Score: 2},
		{ID: personal.ID, Version: 1, Score: 1.5},
		{ID: project.ID, Version: 1, Score: 1},
	}
	reads := store.chunkReadCount()

	results, err := sources.Search(ctx, "notes", retrieval.WithScopes("project"))
	if err != nil {
		t.Fatal(err)
	}

	if len(results) != 1 || results[0].Chunk != project || results[0].Score != 3 {
		t.Fatalf("results = %+v, want the project chunk once", results)
	}

	if store.chunkReadCount()-reads != 1 {
		t.Fatalf("store chunk reads = %d, want 1", store.chunkReadCount()-reads)
	}
}

func TestSourcesSearchWithoutCandidatesSkipsStore(t *testing.T) {
	t.Parallel()

	store := newFakeStore()
	sources := newSources(t, store, source.WithRetriever(retrieverFunc(func(context.Context, retrieval.Query) ([]retrieval.Candidate, error) {
		return nil, nil
	})))

	results, err := sources.Search(t.Context(), "alpha")
	if err != nil || results == nil || len(results) != 0 || store.chunkReadCount() != 0 {
		t.Fatalf("Search() = %#v, %v, chunk reads = %d", results, err, store.chunkReadCount())
	}
}

func TestSourcesSearchPropagatesFailures(t *testing.T) {
	t.Parallel()

	failure := errors.New("unavailable")

	failing := newSources(t, newFakeStore(), source.WithRetriever(retrieverFunc(func(context.Context, retrieval.Query) ([]retrieval.Candidate, error) {
		return nil, failure
	})))
	if _, err := failing.Search(t.Context(), "alpha"); !errors.Is(err, failure) {
		t.Fatalf("Search(retriever failure) error = %v, want %v", err, failure)
	}

	store := newFakeStore()
	store.failChunks(failure)

	unreadable := newSources(t, store, source.WithRetriever(retrieverFunc(func(context.Context, retrieval.Query) ([]retrieval.Candidate, error) {
		return []retrieval.Candidate{{ID: "a", Score: 1}}, nil
	})))
	if results, err := unreadable.Search(t.Context(), "alpha"); !errors.Is(err, failure) || results != nil {
		t.Fatalf("Search(store failure) = %+v, %v, want %v without results", results, err, failure)
	}
}

func TestNewRechunksDocumentsWhoseChunkerChanged(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	store := newFakeStore()

	previous := newSources(t, store, source.WithChunker("text/other", paragraphChunker{id: "other/v1"}))
	if _, err := previous.Put(ctx, "notes", "project", mediaType, []byte("alpha\n\nbeta")); err != nil {
		t.Fatal(err)
	}

	if _, err := previous.Put(ctx, "other", "project", "text/other", []byte("gamma\n\ndelta")); err != nil {
		t.Fatal(err)
	}

	index := newRecordingIndex()
	for item, err := range source.Items(ctx, store) {
		if err != nil {
			t.Fatal(err)
		}

		if err := index.Upsert(ctx, item); err != nil {
			t.Fatal(err)
		}
	}

	stale := chunkIDs(store.documentChunks("notes"))
	other := chunkIDs(store.documentChunks("other"))

	if _, err := source.New(ctx, store,
		source.WithChunker(mediaType, wholeChunker{}),
		source.WithIndexer(index, failOnIndexError(t)),
	); err != nil {
		t.Fatal(err)
	}

	document, err := store.Get(ctx, "notes")
	if err != nil || document.Chunker != "whole/v1" {
		t.Fatalf("rechunked document = %+v, %v", document, err)
	}

	chunks := store.documentChunks("notes")
	if len(chunks) != 1 || chunks[0].Content != "alpha\n\nbeta" || !index.contains(chunks[0].ID) || slices.ContainsFunc(stale, index.contains) {
		t.Fatalf("rechunked chunks = %+v, indexed = %v", chunks, index.ids())
	}

	if !slices.Equal(chunkIDs(store.documentChunks("other")), other) || !index.contains(other[0]) {
		t.Fatal("a document without a registered chunker was rechunked")
	}
}

func TestSourcesSerializesWritesOfOneDocument(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	store := newFakeStore()
	index := newRecordingIndex()
	sources := newSources(t, store, source.WithIndexer(index, failOnIndexError(t)))

	var writers sync.WaitGroup
	for writer := range 20 {
		writers.Go(func() {
			content := fmt.Sprintf("shared\n\nwriter %d\n\nwriter %d again", writer, writer%3)
			if _, err := sources.Put(ctx, "notes", "project", mediaType, []byte(content)); err != nil {
				t.Error(err)
			}
		})
	}

	writers.Wait()

	if stored := chunkIDs(store.documentChunks("notes")); !sameIDs(index.ids(), stored) {
		t.Fatalf("indexed = %v, stored = %v, want the index to match the last write", index.ids(), stored)
	}
}

func newSources(t *testing.T, store source.Store, options ...source.Option) *source.Sources {
	t.Helper()

	options = append([]source.Option{source.WithChunker(mediaType, paragraphChunker{id: "paragraphs/v1"})}, options...)

	sources, err := source.New(t.Context(), store, options...)
	if err != nil {
		t.Fatal(err)
	}

	return sources
}

func failOnIndexError(t *testing.T) func(context.Context, error) {
	t.Helper()

	return func(_ context.Context, err error) {
		t.Errorf("index error: %v", err)
	}
}

func chunkIDs(chunks []source.Chunk) []string {
	ids := make([]string, len(chunks))
	for position, chunk := range chunks {
		ids[position] = chunk.ID
	}

	return ids
}

func sameIDs(got, want []string) bool {
	return slices.Equal(slices.Sorted(slices.Values(got)), slices.Sorted(slices.Values(want)))
}

type paragraphChunker struct {
	id  string
	err error
}

func (c paragraphChunker) ID() string {
	return c.id
}

func (c paragraphChunker) Chunk(content []byte) ([]source.Passage, error) {
	if c.err != nil {
		return nil, c.err
	}

	var passages []source.Passage

	for position, text := range strings.Split(string(content), "\n\n") {
		passages = append(passages, source.Passage{Content: text, Location: fmt.Sprintf("part %d", position+1)})
	}

	return passages, nil
}

type wholeChunker struct{}

func (wholeChunker) ID() string {
	return "whole/v1"
}

func (wholeChunker) Chunk(content []byte) ([]source.Passage, error) {
	return []source.Passage{{Content: string(content), Location: "whole"}}, nil
}

type retrieverFunc func(context.Context, retrieval.Query) ([]retrieval.Candidate, error)

func (f retrieverFunc) Retrieve(ctx context.Context, query retrieval.Query) ([]retrieval.Candidate, error) {
	return f(ctx, query)
}

type recordingIndex struct {
	mu      sync.Mutex
	items   map[string]retrieval.Item
	upserts []string
	removes []string
	err     error
}

func newRecordingIndex() *recordingIndex {
	return &recordingIndex{items: make(map[string]retrieval.Item)}
}

func (i *recordingIndex) Upsert(_ context.Context, item retrieval.Item) error {
	i.mu.Lock()
	defer i.mu.Unlock()

	if i.err != nil {
		return i.err
	}

	i.items[item.ID] = item
	i.upserts = append(i.upserts, item.ID)

	return nil
}

func (i *recordingIndex) Remove(_ context.Context, id string) error {
	i.mu.Lock()
	defer i.mu.Unlock()

	if i.err != nil {
		return i.err
	}

	delete(i.items, id)
	i.removes = append(i.removes, id)

	return nil
}

func (i *recordingIndex) fail(err error) {
	i.mu.Lock()
	defer i.mu.Unlock()

	i.err = err
}

func (i *recordingIndex) item(id string) retrieval.Item {
	i.mu.Lock()
	defer i.mu.Unlock()

	return i.items[id]
}

func (i *recordingIndex) contains(id string) bool {
	i.mu.Lock()
	defer i.mu.Unlock()

	_, found := i.items[id]

	return found
}

func (i *recordingIndex) ids() []string {
	i.mu.Lock()
	defer i.mu.Unlock()

	return slices.Sorted(maps.Keys(i.items))
}

func (i *recordingIndex) upserted() []string {
	i.mu.Lock()
	defer i.mu.Unlock()

	return slices.Clone(i.upserts)
}

func (i *recordingIndex) removed() []string {
	i.mu.Lock()
	defer i.mu.Unlock()

	return slices.Clone(i.removes)
}

type fakeStore struct {
	mu         sync.Mutex
	documents  map[string]source.Document
	chunks     map[string][]source.Chunk
	puts       int
	chunkReads int
	chunksErr  error
}

func newFakeStore() *fakeStore {
	return &fakeStore{
		documents: make(map[string]source.Document),
		chunks:    make(map[string][]source.Chunk),
	}
}

func (s *fakeStore) Get(_ context.Context, id string) (source.Document, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	document, found := s.documents[id]
	if !found {
		return source.Document{}, source.ErrNotFound
	}

	return document, nil
}

func (s *fakeStore) List(_ context.Context, filter source.Filter) (source.Page, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	limit := cmp.Or(filter.Limit, source.DefaultPageSize)

	page := source.Page{Documents: []source.Document{}}
	for _, id := range slices.Sorted(maps.Keys(s.documents)) {
		document := s.documents[id]
		if id <= filter.AfterID || !matches(filter.Scopes, document.Scope) || !matches(filter.IDs, id) {
			continue
		}

		if len(page.Documents) == limit {
			page.NextID = page.Documents[limit-1].ID

			break
		}

		page.Documents = append(page.Documents, document)
	}

	return page, nil
}

func (s *fakeStore) Put(_ context.Context, document source.Document, chunks []source.Chunk) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.puts++
	s.documents[document.ID] = document
	s.chunks[document.ID] = slices.Clone(chunks)

	return nil
}

func (s *fakeStore) Delete(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, found := s.documents[id]; !found {
		return source.ErrNotFound
	}

	delete(s.documents, id)
	delete(s.chunks, id)

	return nil
}

func (s *fakeStore) Chunks(_ context.Context, filter source.ChunkFilter) (source.ChunkPage, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.chunkReads++
	if s.chunksErr != nil {
		return source.ChunkPage{}, s.chunksErr
	}

	var all []source.Chunk
	for _, chunks := range s.chunks {
		all = append(all, chunks...)
	}

	slices.SortFunc(all, func(a, b source.Chunk) int {
		return strings.Compare(a.ID, b.ID)
	})

	limit := cmp.Or(filter.Limit, source.DefaultPageSize)

	page := source.ChunkPage{Chunks: []source.Chunk{}}
	for _, chunk := range all {
		if chunk.ID <= filter.AfterID || !matches(filter.DocumentIDs, chunk.DocumentID) || !matches(filter.IDs, chunk.ID) {
			continue
		}

		if len(page.Chunks) == limit {
			page.NextID = page.Chunks[limit-1].ID

			break
		}

		page.Chunks = append(page.Chunks, chunk)
	}

	return page, nil
}

func (s *fakeStore) documentChunks(id string) []source.Chunk {
	s.mu.Lock()
	defer s.mu.Unlock()

	return slices.Clone(s.chunks[id])
}

func (s *fakeStore) putCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.puts
}

func (s *fakeStore) chunkReadCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.chunkReads
}

func (s *fakeStore) failChunks(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.chunksErr = err
}

func matches(allowed []string, value string) bool {
	return len(allowed) == 0 || slices.Contains(allowed, value)
}
