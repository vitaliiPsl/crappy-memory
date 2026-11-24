# Crappy Memory — Knowledge & Context Ingestion Specification

**Status:** Proposed  
**Scope:** Document ingestion, chunking, knowledge extraction, provenance, and combined retrieval  
**Architecture:** Extension of the existing memory library

---

## 1. Vision

Crappy Memory is a modular, local-first knowledge and context library for AI applications.

It supports two complementary forms of information:

- **Context:** Original information ingested from documents, code, conversations, and other sources.
- **Knowledge:** Independent, distilled facts, decisions, rules, and learned information.

Applications can preserve context, derive knowledge from it, and retrieve both when necessary.

The system is domain-independent. Coding agents, personal assistants, research agents, and business knowledge assistants should be able to use the same infrastructure.

The library does not own agent orchestration, execution scheduling, or autonomous decision-making.

## 2. Architectural foundation

The existing Crappy Memory implementation remains the foundation.

It already provides:

- Canonical `Entry` records and a `Memory` facade.
- Versioned CRUD operations.
- JSON and SQLite stores.
- BM25 and vector indexes.
- Local embedding support.
- Hybrid retrieval through RRF.
- Candidate hydration and stale-version filtering.
- Index initialization and reconciliation.

The ingestion extension must reuse these capabilities wherever practical.

**The existing `Entry` model remains unchanged and reserved for independent knowledge.**

Document-specific fields and lifecycle behavior must not be added to `Entry`.

The library must remain usable without configuring ingestion, document storage, or knowledge extraction.

## 3. Overall architecture

The system consists of four cooperating areas, shown in the accompanying single architecture diagram.

**Ingestion and processing** accepts sources, prepares documents, creates chunks, and optionally derives candidate knowledge.

**Canonical persistence** maintains two logical stores: one for documents and chunks, and one for accepted memory entries.

**Indexing and retrieval** maintains replaceable search representations and retrieves current records from their authoritative stores.

**Applications** decide what information to ingest, which extracted knowledge to accept, and how retrieved context is used.

A document does not need to produce knowledge entries to become searchable.

Likewise, memory entries can be created directly without originating from an ingested document.

## 4. Data model

### 4.1 Entry

An `Entry` represents independently retained knowledge.

Examples include facts, preferences, architectural decisions, business rules, procedures, and lessons.

The current model and API are preserved.

An entry may reference supporting source information but has an independent lifecycle.

### 4.2 Document

A `Document` represents an ingested snapshot of source information.

Conceptual model:

```go
type Document struct {
    ID          string
    Scope       string
    SourceURI   string
    MediaType   string
    Content     []byte
    ContentHash string
    Revision    string
}
```

Additional metadata, timestamps, and version fields may be added following existing conventions.

Requirements:

- Stable logical identity within its scope.
- Original source content preserved.
- Content fingerprint for change detection.
- Optional external revision metadata.
- Support for replacement and deletion.
- No dependence on a particular source provider.

The content hash identifies stored content. An external revision, such as a Git commit, is provenance metadata and is not necessarily equivalent to the content hash.

Historical revisions need not be retained in V1.

### 4.3 Chunk

A `Chunk` is a searchable passage generated from a document.

Conceptual model:

```go
type Chunk struct {
    ID           string
    DocumentID   string
    Content      string
    Position     int
    ContentHash  string
    DocumentHash string
    ProcessorID  string
}
```

Optional location metadata includes section paths and source offsets.

Requirements:

- Each chunk belongs to one document.
- Chunks are derived and rebuildable.
- Chunks are persisted for efficient retrieval.
- Chunks are not memory entries.
- Each active chunk belongs to the current document processing generation.
- Source provenance must be recoverable from a chunk.

Chunk identities may be preserved for unchanged passages, but stable chunk IDs across document revisions are not required for V1.

## 5. Persistence

Two logical stores are maintained.

### Memory Store

The existing canonical store for `Entry` records.

It continues to support existing versioned CRUD and indexing behavior.

### Document Store

A new canonical storage interface responsible for:

- Document records.
- Derived chunk records.
- Document-to-chunk ownership.
- Source revisions and content fingerprints.
- Processing configuration identity.
- Active processing generation and completion state.

The Document Store replaces the need for a separate manifest store.

Documents and chunks should be managed together so source-level replacement and deletion can be implemented safely.

The initial backend should be SQLite. The document and memory stores may share the same physical database.

The JSON Memory Store remains supported and unchanged.

## 6. Ingestion

Ingestion follows this lifecycle:

**Receive → Identify → Parse → Chunk → Reconcile → Persist → Index**

### 6.1 Receive

The caller supplies document content, scope, source identity, and media type.

The first implementation accepts content directly rather than requiring a filesystem or external-source connector.

### 6.2 Identify

The ingestor resolves the logical document identity and calculates its content fingerprint.

If the source content and processing configuration are unchanged and the previous processing completed successfully, reingestion should be idempotent.

### 6.3 Parse

Parsers convert source content into structured text suitable for chunking.

V1 supports:

- Plain text.
- Markdown.

Parsers should preserve meaningful document structure, including headings, paragraphs, lists, and code blocks.

Parsers do not own persistence or indexing.

### 6.4 Chunk

Chunking converts parsed content into meaningful, independently retrievable passages.

The initial implementation uses deterministic, structure-aware chunking.

Requirements:

- Prefer document boundaries and headings.
- Preserve sufficient structural context.
- Avoid unnecessary splits within paragraphs or code blocks.
- Split oversized sections.
- Enforce configurable size limits.
- Record passage location where possible.
- Return consistent results for identical input and configuration.

Initial target: approximately 400–800 tokens per chunk, with a configurable maximum around 1,000.

These are starting heuristics, not mandatory sizes for every source.

Overlap is disabled by default.

Each processor has a stable configuration identity used for reprocessing decisions.

### 6.5 Reconcile

Reconciliation compares newly processed document content and chunks against the current active representation.

V1 may replace the complete chunk set on every changed revision.

Incremental preservation of unchanged chunks and embeddings is an optimization for later.

### 6.6 Persist

The Document Store commits the updated document, its chunks, and active processing state.

Where supported, this should be atomic.

A store that cannot atomically replace the complete active document/chunk set must provide recoverable generation semantics so partially applied updates are not exposed as complete.

### 6.7 Index

Once canonical document state is committed, chunk indexes are updated.

Indexing failures must not corrupt document storage.

Indexes remain derived and independently rebuildable.

Ingestion must distinguish successful persistence from fully synchronized search indexes.

## 7. Document retrieval

Document retrieval searches chunk content, not entire raw documents by default.

Results contain:

- Relevant passage content.
- Document identity.
- Source URI and content revision.
- Chunk identity and location.
- Retrieval score.

BM25 and vector search implementations should be reused where practical.

The existing memory pipeline currently assumes `Entry` records and `memory.Store`; therefore document retrieval will require adapted candidate resolution.

Avoid introducing a universal record abstraction prematurely.

Retrieval must verify candidate validity against canonical document state so deleted or superseded chunks are not returned as current information.

Scope and source constraints must be enforced against canonical records.

## 8. Knowledge extraction

Knowledge extraction is an **optional processing stage**.

It transforms source context into candidate knowledge.

The initial implementation may process a document using bounded windows of related chunks.

Extraction is not restricted to one chunk at a time.

The extractor can be implemented using an LLM, specialized model, deterministic rules, or another processing approach.

No model provider is required by the core library.

### Candidate representation

```go
type KnowledgeCandidate struct {
    Content  string
    Evidence []EvidenceRef
}
```

Evidence references identify the source documents, revisions, and passages supporting a claim.

A good candidate should be:

- Self-contained.
- Grounded in explicit evidence.
- Reusable.
- Focused on one coherent fact or rule.
- Precise about names and identifiers.

The extractor may produce no candidates.

**Extractors do not directly persist memory entries.**

## 9. Knowledge resolution and acceptance

Candidates are processed separately from extraction.

The system should support decisions such as:

- **Accept:** The candidate contributes new knowledge.
- **Skip:** The candidate is duplicate or unnecessary.
- **Review:** The candidate is unsupported, ambiguous, or contradictory.

An additional fact about the same entity is not automatically a duplicate.

Contradictory source information must not automatically overwrite existing knowledge.

Initial resolution may be manual or use simple automated filtering.

Accepted candidates are persisted through the existing `Memory` API.

Full automatic memory consolidation and contradiction resolution are deferred.

## 10. Provenance

Provenance connects knowledge to supporting context.

The model must distinguish:

**Ownership:** A document owns its generated chunks.

**Evidence:** A document or chunk supports an independently retained knowledge entry.

Evidence should identify the source document and its revision or content fingerprint, along with the relevant passage where available.

Current entry metadata and source-reference fields may be used initially.

The system must not silently treat a current document revision as evidence for a fact extracted from an earlier revision.

A historical reference may become unresolved if the earlier source content was not retained.

Deleting a source does not automatically delete independently accepted knowledge.

## 11. Updates and synchronization

### Identical ingestion

An unchanged source and unchanged processing configuration should not create duplicate documents or chunks.

### Changed source

The system reprocesses the document, replaces its active derived chunks, and updates associated indexes.

Existing accepted memories remain unchanged.

### Changed processor

Changes to parser or chunker configuration require reprocessing where the resulting representation may differ.

### Source deletion

Deleting a document removes its owned chunks and invalidates corresponding derived indexes.

### Failed processing

An invalid document or processing failure must not replace successfully stored current content with incomplete output.

### Interrupted updates

A retry must repair incomplete operations without permanently duplicating active chunks.

Concurrent ingestion of the same logical document must be serialized or rejected through concurrency control.

### Index inconsistency

The existing store-first, index-second model remains applicable.

Index updates can fail without rolling back committed canonical data, but affected records may temporarily be missing from search.

Index reconciliation must restore search consistency.

## 12. Combined retrieval

The system should eventually support retrieving both:

- Independent memory entries.
- Source document chunks.

Combined results retain their distinct types and provenance.

No unified ranking strategy is mandated in V1.

The system may initially run separate memory and document retrieval pipelines and combine results at the application-facing layer.

The shared infrastructure should be generalized only where actual implementation reuse requires it.

## 13. Extensibility

The initial architecture must allow future capabilities without requiring them in the core.

Potential extensions include:

- PDF and HTML parsing.
- Code-aware chunking.
- Source connectors and synchronization.
- Cross-document knowledge extraction.
- Summarization.
- Knowledge deduplication and consolidation.
- Provenance-driven revalidation.
- More advanced retrieval strategies.

These should be optional processing or retrieval components.

## 14. Out of scope

The first implementation explicitly excludes:

- Knowledge graphs.
- Automatic entity/relationship graph construction.
- Agent orchestration.
- Background job scheduling.
- General-purpose DAG pipeline execution.
- Automatic contradiction resolution.
- External connectors.
- Distributed storage.
- Complex multi-tenant authorization infrastructure.
- LLM-based semantic chunking.
- Historical document version archives.

## 15. Implementation milestones

### Milestone 1 — Document foundation

Implement:

- Document and chunk models.
- Document Store and SQLite implementation.
- Source identity and content fingerprints.
- Plain-text and Markdown parsing.
- Deterministic chunking.
- Document ingestion, replacement, and deletion.
- Chunk indexing and source-aware retrieval.
- Idempotency and interrupted-ingestion recovery.

**Definition of done:** Documents can be ingested, searched, replaced, and deleted without creating permanently inconsistent active chunk sets.

### Milestone 2 — Knowledge extraction

Implement:

- Optional extractor contract.
- Bounded extraction context.
- Knowledge candidates.
- Evidence references.
- Explicit acceptance into the existing Memory Store.
- Minimal duplicate handling.

**Definition of done:** A consumer can extract grounded candidate knowledge from documents and selectively retain it as independent entries.

### Milestone 3 — Combined retrieval

Implement retrieval that can return document context and independent knowledge in the same application workflow.

Preserve provenance, scope, and distinct result types.

**Definition of done:** An agent can retrieve both stored understanding and its supporting source information without requiring every document to produce knowledge entries.

### Milestone 4 — Knowledge maintenance (deferred)

Investigate:

- Source-change impact detection.
- Knowledge revalidation.
- Contradiction resolution.
- Incremental extraction.
- Memory consolidation.

No implementation is required in the initial ingestion milestone.

## 16. Architectural principles

1. Preserve the existing memory foundation.
2. Keep `Entry` reserved for independent knowledge.
3. Treat documents as canonical source information.
4. Treat chunks and indexes as derived representations.
5. Separate ingestion from optional knowledge extraction.
6. Separate extraction from knowledge acceptance.
7. Preserve provenance and explicit ownership relationships.
8. Allow components to be replaced independently.
9. Keep ingestion optional for existing memory consumers.
10. Prefer small concrete interfaces over generalized frameworks.

---

## Final objective

Crappy Memory should allow an application to preserve what it has encountered, retrieve the relevant source material, distill useful knowledge from it, and reuse that knowledge across future interactions.

The same foundation should support coding agents, personal assistants, research workflows, and other knowledge-intensive applications without imposing a particular agent framework or processing strategy.