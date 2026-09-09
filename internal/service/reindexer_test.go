package service

import (
	"context"
	"errors"
	"io"
	"log"
	"testing"

	"github.com/verdu/alter/internal/domain"
)

// orderedTaskRepo is a slice-backed TaskRepository whose List order is
// deterministic (the map-backed fakeTaskRepo is not).
type orderedTaskRepo struct {
	tasks []domain.Task
	err   error
}

func (r *orderedTaskRepo) Create(context.Context, domain.Task) error { return nil }
func (r *orderedTaskRepo) GetByID(context.Context, string) (domain.Task, error) {
	return domain.Task{}, nil
}
func (r *orderedTaskRepo) Update(context.Context, domain.Task) error   { return nil }
func (r *orderedTaskRepo) Delete(context.Context, string) error        { return nil }
func (r *orderedTaskRepo) List(context.Context) ([]domain.Task, error) { return r.tasks, r.err }

// eventListRepo is a list-only EventStore fake for source-of-truth reads.
type eventListRepo struct {
	events []domain.Event
	err    error
}

func (r *eventListRepo) Save(context.Context, domain.Event) error { return nil }
func (r *eventListRepo) ListByType(_ context.Context, t domain.EventType) ([]domain.Event, error) {
	if r.err != nil {
		return nil, r.err
	}
	var out []domain.Event
	for _, e := range r.events {
		if e.Type == t {
			out = append(out, e)
		}
	}
	return out, nil
}

// recordingIndexer records the exact sequence of index operations.
type recordingIndexer struct {
	order    []string
	resetErr error
	taskErr  map[string]error
	eventErr map[string]error
}

func (r *recordingIndexer) Reset(context.Context) error {
	r.order = append(r.order, "reset")
	return r.resetErr
}
func (r *recordingIndexer) IndexTask(_ context.Context, t domain.Task) error {
	r.order = append(r.order, "task:"+t.ID)
	if err := r.taskErr[t.ID]; err != nil {
		return err
	}
	return nil
}
func (r *recordingIndexer) RemoveTask(context.Context, string) error { return nil }
func (r *recordingIndexer) IndexEvent(_ context.Context, e domain.Event) error {
	r.order = append(r.order, "event:"+e.ID)
	if err := r.eventErr[e.ID]; err != nil {
		return err
	}
	return nil
}

func newReindexerHarness() (*Reindexer, *orderedTaskRepo, *eventListRepo, *recordingIndexer) {
	tasks := &orderedTaskRepo{}
	events := &eventListRepo{}
	idx := &recordingIndexer{}
	r := NewReindexer(tasks, events, idx, WithReindexerLogger(log.New(io.Discard, "", 0)))
	return r, tasks, events, idx
}

func TestRebuildOrderAndSkipsOtherEvents(t *testing.T) {
	ctx := context.Background()
	r, tasks, events, idx := newReindexerHarness()

	tasks.tasks = []domain.Task{
		{ID: "t1", Title: "alpha"},
		{ID: "t2", Title: "beta"},
	}
	events.events = []domain.Event{
		{ID: "e1", Type: domain.EventAgentResult, Payload: map[string]any{"response": "first"}},
		{ID: "e2", Type: domain.EventTaskCreated}, // must NOT be indexed
		{ID: "e3", Type: domain.EventAgentResult, Payload: map[string]any{"response": "second"}},
	}

	if err := r.Rebuild(ctx); err != nil {
		t.Fatalf("rebuild: %v", err)
	}

	want := []string{"reset", "task:t1", "task:t2", "event:e1", "event:e3"}
	if len(idx.order) != len(want) {
		t.Fatalf("order = %v, want %v", idx.order, want)
	}
	for i := range want {
		if idx.order[i] != want[i] {
			t.Fatalf("order = %v, want %v", idx.order, want)
		}
	}
}

func TestRebuildResetErrorAborts(t *testing.T) {
	ctx := context.Background()
	r, tasks, _, idx := newReindexerHarness()
	tasks.tasks = []domain.Task{{ID: "t1", Title: "alpha"}}
	idx.resetErr = errors.New("db locked")

	err := r.Rebuild(ctx)
	if err == nil {
		t.Fatal("expected error on Reset failure")
	}
	if len(idx.order) != 1 || idx.order[0] != "reset" {
		t.Errorf("order = %v, want [reset] (nothing indexed after Reset failure)", idx.order)
	}
}

func TestRebuildTaskListErrorAborts(t *testing.T) {
	ctx := context.Background()
	r, tasks, _, idx := newReindexerHarness()
	tasks.err = errors.New("repo down")

	if err := r.Rebuild(ctx); err == nil {
		t.Fatal("expected error on task list failure")
	}
	if len(idx.order) != 1 || idx.order[0] != "reset" {
		t.Errorf("order = %v, want [reset]", idx.order)
	}
}

func TestRebuildIndexTaskErrorStopsMidway(t *testing.T) {
	ctx := context.Background()
	r, tasks, events, idx := newReindexerHarness()
	tasks.tasks = []domain.Task{{ID: "t1", Title: "alpha"}, {ID: "t2", Title: "beta"}}
	events.events = []domain.Event{{ID: "e1", Type: domain.EventAgentResult, Payload: map[string]any{"response": "x"}}}
	idx.taskErr = map[string]error{"t2": errors.New("embedder down")}

	if err := r.Rebuild(ctx); err == nil {
		t.Fatal("expected error when IndexTask fails midway")
	}
	// Partial state is accepted (fully derived): the next boot's Reset+reindex
	// converges. Only the failing step must be the last one executed.
	want := []string{"reset", "task:t1", "task:t2"}
	if len(idx.order) != len(want) {
		t.Fatalf("order = %v, want %v (partial index acceptable)", idx.order, want)
	}
}

func TestRebuildEventErrorStopsAfterTasks(t *testing.T) {
	ctx := context.Background()
	r, tasks, events, idx := newReindexerHarness()
	tasks.tasks = []domain.Task{{ID: "t1", Title: "alpha"}}
	events.events = []domain.Event{{ID: "e1", Type: domain.EventAgentResult, Payload: map[string]any{"response": "x"}}}
	idx.eventErr = map[string]error{"e1": errors.New("embedder down")}

	if err := r.Rebuild(ctx); err == nil {
		t.Fatal("expected error when IndexEvent fails")
	}
	want := []string{"reset", "task:t1", "event:e1"}
	if len(idx.order) != len(want) {
		t.Fatalf("order = %v, want %v", idx.order, want)
	}
}
