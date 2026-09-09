package service

import (
	"context"
	"fmt"
	"io"
	"log"

	"github.com/verdu/alter/internal/domain"
)

// Reindexer owns full reconstruction of the derived semantic index from the
// SQLite source of truth. It is the ONLY component allowed to Reset the index:
// TaskService and the Orchestrator only do best-effort write-through sync and
// must never wipe it.
//
// Rebuild is idempotent and safe to run at every boot: it starts by Reset
// (wiping the index, so orphaned rows left by a failed RemoveTask cannot
// survive), then re-embeds every task and every agent.result event. A failure
// halfway leaves the index partial; because the index is fully derived, the
// next boot's Rebuild (Reset + reindex) converges. Reads over a partial index
// fail open, so a broken rebuild never breaks Tasks, Scheduler, Telegram or
// the Pi Agent.
type Reindexer struct {
	tasks   domain.TaskRepository
	events  domain.EventStore
	indexer domain.SemanticIndexer
	logger  *log.Logger
}

// ReindexerOption configures a Reindexer (diagnostics).
type ReindexerOption func(*Reindexer)

// WithReindexerLogger sets the logger used for diagnostics.
func WithReindexerLogger(l *log.Logger) ReindexerOption {
	return func(r *Reindexer) { r.logger = l }
}

// NewReindexer builds a Reindexer over the source-of-truth ports and the
// derived-index write port.
func NewReindexer(tasks domain.TaskRepository, events domain.EventStore, indexer domain.SemanticIndexer, opts ...ReindexerOption) *Reindexer {
	r := &Reindexer{
		tasks:   tasks,
		events:  events,
		indexer: indexer,
		logger:  log.New(io.Discard, "", 0),
	}
	for _, o := range opts {
		o(r)
	}
	return r
}

// Rebuild reconstructs the index exactly as a projection of SQLite:
//
//  1. Reset the whole index (removes orphans and any partial state);
//  2. Index every task via TaskRepository.List;
//  3. Index every agent.result event via EventStore.ListByType.
//
// Any step failure returns the error immediately: the index may be left
// partial, which is acceptable (fully derived) and the next boot's Rebuild
// converges again.
func (r *Reindexer) Rebuild(ctx context.Context) error {
	if err := r.indexer.Reset(ctx); err != nil {
		return fmt.Errorf("reindexer: reset index: %w", err)
	}

	tasks, err := r.tasks.List(ctx)
	if err != nil {
		return fmt.Errorf("reindexer: list tasks: %w", err)
	}
	for _, t := range tasks {
		if err := r.indexer.IndexTask(ctx, t); err != nil {
			return fmt.Errorf("reindexer: index task %s: %w", t.ID, err)
		}
	}

	events, err := r.events.ListByType(ctx, domain.EventAgentResult)
	if err != nil {
		return fmt.Errorf("reindexer: list agent result events: %w", err)
	}
	for _, e := range events {
		if err := r.indexer.IndexEvent(ctx, e); err != nil {
			return fmt.Errorf("reindexer: index event %s: %w", e.ID, err)
		}
	}

	r.logger.Printf("reindexer: rebuilt semantic index from source of truth (%d tasks, %d agent results)", len(tasks), len(events))
	return nil
}
