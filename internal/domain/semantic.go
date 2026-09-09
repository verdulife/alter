package domain

import (
	"context"
	"errors"
)

// ErrSemanticUnavailable reports that the semantic index cannot serve a request:
// no searcher/indexer is wired, or the embedding provider is missing or failed.
// Callers MUST treat it as fail-open: skip context enrichment / index sync and
// let the authoritative operation (task write, trigger fire, delivery) proceed
// unchanged. It is intentionally the only sentinel the domain exposes for the
// semantic capability.
var ErrSemanticUnavailable = errors.New("semantic index unavailable")

// RefKind discriminates the entity kinds indexed by the derived semantic index.
type RefKind string

const (
	// RefKindTask indexes a Task through its text projection (title,
	// description, source). SQLite remains the source of truth; the projection
	// is owned by the search adapter.
	RefKindTask RefKind = "task"
	// RefKindEvent indexes an agent.result Event: a past agent response that is
	// retrievable as memory/continuity context for future fires.
	RefKindEvent RefKind = "event"
)

// Ref identifies one indexed entity. It is the only key the domain layer
// exposes; the concrete index rows (search_docs) are an adapter detail.
type Ref struct {
	Kind RefKind
	ID   string
}

// SearchOptions is the caller-supplied control surface of a search.
type SearchOptions struct {
	// Limit caps the number of results. 0 means "use the adapter's configured
	// default" (runtime default 5).
	Limit int
	// Exclude removes specific refs from the result set regardless of
	// relevance, e.g. the task currently being executed so its own text cannot
	// crowd out genuinely related context.
	Exclude []Ref
}

// SearchResult is one ranked hit of a semantic search.
type SearchResult struct {
	// Ref identifies the hit (kind + id of the indexed entity).
	Ref Ref
	// Text is the indexed text of the hit: the task projection or the agent
	// response. Consumers format it for presentation; the domain Task/Event
	// objects are deliberately NOT returned (the index is the projection).
	Text string
	// Score is a backend-specific relative relevance measure: higher = more
	// relevant. Only the ORDER of results is meaningful; the absolute value is
	// never comparable across backends or queries (e.g. raw cosine similarity
	// in the SQLite adapter).
	Score float64
}

// SemanticSearcher is the read side of the semantic search capability: retrieve
// the most relevant indexed entities for a query, ranked by relevance.
//
// It is deliberately outbound: consumers (e.g. the agent Orchestrator) depend on
// this interface and never on the concrete vector store, SQLite or an embedding
// provider. The Scheduler and the Pi Agent adapter must never depend on it.
type SemanticSearcher interface {
	Search(ctx context.Context, query string, opts SearchOptions) ([]SearchResult, error)
}

// SemanticIndexer is the write side of the derived semantic index.
//
// The index is a DERIVED projection: SQLite tables (tasks, events) remain the
// source of truth, and the whole index can be Reset and rebuilt at any time.
// Every method MUST fail-open at the caller: an index error never breaks the
// authoritative write that triggered it. The only component allowed to Reset is
// the application-layer Reindexer.
type SemanticIndexer interface {
	// Reset wipes the entire derived index. It is idempotent and deliberately
	// destructive: it is the first step of a full rebuild so orphaned rows
	// (e.g. left by a failed RemoveTask) cannot survive.
	Reset(ctx context.Context) error
	// IndexTask upserts the derived text projection of a task.
	IndexTask(ctx context.Context, task Task) error
	// RemoveTask deletes the task's index entry (best-effort: a failure leaves
	// an orphan that the next Reset+rebuild removes).
	RemoveTask(ctx context.Context, taskID string) error
	// IndexEvent upserts an agent.result Event (its response payload) as
	// retrievable agent memory. It is an error to pass any other event type.
	IndexEvent(ctx context.Context, event Event) error
}
