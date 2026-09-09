package semantic_test

import (
	"context"
	"database/sql"
	"errors"
	"hash/fnv"
	"math"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"

	"github.com/verdu/alter/internal/adapter/semantic"
	"github.com/verdu/alter/internal/domain"
	"github.com/verdu/alter/internal/storage/sqlite"
)

// fakeEmbedder returns deterministic vectors: identical text → identical vector
// (cosine ≈ 1), different text → different direction. No real model involved.
type fakeEmbedder struct {
	dim int
	err error
}

func (f *fakeEmbedder) Embed(_ context.Context, texts []string) ([][]float32, error) {
	if f.err != nil {
		return nil, f.err
	}
	out := make([][]float32, len(texts))
	for i, t := range texts {
		out[i] = fakeVector(t, f.dim)
	}
	return out, nil
}

func (f *fakeEmbedder) Name() string { return "fake" }

func fakeVector(text string, dim int) []float32 {
	h := fnv.New64a()
	h.Write([]byte(text))
	seed := h.Sum64()
	v := make([]float32, dim)
	for i := range v {
		v[i] = float32(math.Sin(float64(seed)*float64(i+1)*1.618033988749895 + float64(seed)*0.618033988749895))
	}
	return v
}

// --- Harness -----------------------------------------------------------------

// openStore opens a real SQLite store (migrations included) and returns the
// semantic adapter plus the DB file path (for raw-table assertions).
func openStore(t *testing.T, embedder semantic.Embedder, opts ...semantic.Option) (*semantic.Store, string) {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "semantic.db")
	st, err := sqlite.Open(dbPath)
	if err != nil {
		t.Fatalf("open sqlite store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st.NewSemanticStore(embedder, opts...), dbPath
}

func newTask(id, title, desc string) domain.Task {
	return domain.Task{ID: id, Title: title, Description: desc}
}

// rawRows counts search_docs rows through a second connection, verifying the
// derived projection table directly (bypassing the adapter API).
func rawRows(t *testing.T, dbPath string) int {
	t.Helper()
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open raw db: %v", err)
	}
	defer db.Close()
	var n int
	if err := db.QueryRow(`SELECT COUNT(1) FROM search_docs`).Scan(&n); err != nil {
		t.Fatalf("count search_docs: %v", err)
	}
	return n
}

// --- Nil embedder ------------------------------------------------------------

func TestNilEmbedderDegradesToUnavailable(t *testing.T) {
	ctx := context.Background()
	s, _ := openStore(t, nil)

	// Reset works without an embedder (it never needs vectors)...
	if err := s.Reset(ctx); err != nil {
		t.Fatalf("Reset with nil embedder: %v", err)
	}
	// ...but every embedding-touching operation degrades fail-open.
	if err := s.IndexTask(ctx, newTask("t1", "alpha", "")); !errors.Is(err, domain.ErrSemanticUnavailable) {
		t.Errorf("IndexTask err = %v, want ErrSemanticUnavailable", err)
	}
	if err := s.IndexEvent(ctx, domain.Event{ID: "e1", Type: domain.EventAgentResult, Payload: map[string]any{"response": "x"}}); !errors.Is(err, domain.ErrSemanticUnavailable) {
		t.Errorf("IndexEvent err = %v, want ErrSemanticUnavailable", err)
	}
	if _, err := s.Search(ctx, "alpha", domain.SearchOptions{}); !errors.Is(err, domain.ErrSemanticUnavailable) {
		t.Errorf("Search err = %v, want ErrSemanticUnavailable", err)
	}
}

// --- Index + Search round trips ----------------------------------------------

func TestIndexAndSearchRoundTrip(t *testing.T) {
	ctx := context.Background()
	s, _ := openStore(t, &fakeEmbedder{dim: 8})

	if err := s.IndexTask(ctx, newTask("t1", "buy milk", "urgent")); err != nil {
		t.Fatalf("index t1: %v", err)
	}
	if err := s.IndexTask(ctx, newTask("t2", "pay rent", "landlord")); err != nil {
		t.Fatalf("index t2: %v", err)
	}

	results, err := s.Search(ctx, "buy milk", domain.SearchOptions{Limit: 5})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("got %d results, want 2", len(results))
	}
	top := results[0]
	if top.Ref != (domain.Ref{Kind: domain.RefKindTask, ID: "t1"}) {
		t.Errorf("top result = %+v, want task t1", top.Ref)
	}
	if top.Text != "buy milk\nurgent\n" {
		t.Errorf("top text = %q, want the task projection", top.Text)
	}
	// Score is raw cosine: only the ORDER is meaningful, the value stays in [-1,1].
	if top.Score < -1 || top.Score > 1 {
		t.Errorf("score = %v, want a cosine in [-1,1]", top.Score)
	}
	if results[1].Score > top.Score {
		t.Errorf("results must be ranked by decreasing score: %v then %v", top.Score, results[1].Score)
	}
}

func TestSearchExcludesRefs(t *testing.T) {
	ctx := context.Background()
	s, _ := openStore(t, &fakeEmbedder{dim: 8})

	for _, id := range []string{"t1", "t2"} {
		if err := s.IndexTask(ctx, newTask(id, "alpha beta", "gamma")); err != nil {
			t.Fatalf("index %s: %v", id, err)
		}
	}

	results, err := s.Search(ctx, "alpha beta gamma", domain.SearchOptions{
		Limit:   5,
		Exclude: []domain.Ref{{Kind: domain.RefKindTask, ID: "t1"}},
	})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("got %d results, want 1 (t1 excluded)", len(results))
	}
	if results[0].Ref.ID != "t2" {
		t.Errorf("result = %+v, want t2 after excluding t1", results[0].Ref)
	}
}

func TestSearchLimitDefaultAndOverride(t *testing.T) {
	ctx := context.Background()
	s, _ := openStore(t, &fakeEmbedder{dim: 8}, semantic.WithDefaultLimit(2))

	for _, id := range []string{"t1", "t2", "t3"} {
		if err := s.IndexTask(ctx, newTask(id, "alpha beta gamma delta", "")); err != nil {
			t.Fatalf("index %s: %v", id, err)
		}
	}

	// Limit 0 → the configured default (2).
	results, err := s.Search(ctx, "alpha beta gamma delta", domain.SearchOptions{})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(results) != 2 {
		t.Errorf("Limit 0 → default: got %d results, want 2", len(results))
	}

	// Explicit limit wins.
	results, err = s.Search(ctx, "alpha beta gamma delta", domain.SearchOptions{Limit: 5})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(results) != 3 {
		t.Errorf("explicit limit: got %d results, want 3", len(results))
	}
}

func TestIndexTaskUpsertNoDuplicates(t *testing.T) {
	ctx := context.Background()
	s, dbPath := openStore(t, &fakeEmbedder{dim: 8})

	// Index the same task twice with different text: the row must be replaced,
	// not duplicated.
	if err := s.IndexTask(ctx, newTask("t1", "first title", "")); err != nil {
		t.Fatalf("index first: %v", err)
	}
	if err := s.IndexTask(ctx, newTask("t1", "second title", "changed")); err != nil {
		t.Fatalf("index second: %v", err)
	}
	if n := rawRows(t, dbPath); n != 1 {
		t.Errorf("search_docs rows = %d, want 1 (upsert)", n)
	}

	results, err := s.Search(ctx, "second title changed", domain.SearchOptions{})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(results) != 1 || results[0].Ref.ID != "t1" {
		t.Fatalf("got %+v, want the upserted t1", results)
	}
	if results[0].Text != "second title\nchanged\n" {
		t.Errorf("text = %q, want the last upsert text", results[0].Text)
	}
}

func TestRemoveTask(t *testing.T) {
	ctx := context.Background()
	s, _ := openStore(t, &fakeEmbedder{dim: 8})

	if err := s.IndexTask(ctx, newTask("t1", "alpha", "")); err != nil {
		t.Fatalf("index t1: %v", err)
	}
	if err := s.RemoveTask(ctx, "t1"); err != nil {
		t.Fatalf("remove: %v", err)
	}

	results, err := s.Search(ctx, "alpha", domain.SearchOptions{})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(results) != 0 {
		t.Errorf("got %d results after remove, want 0", len(results))
	}
}

func TestEmptyTaskSkipped(t *testing.T) {
	ctx := context.Background()
	s, _ := openStore(t, &fakeEmbedder{dim: 8})

	// Defensive: a task with nothing indexable adds no row (never an error).
	if err := s.IndexTask(ctx, domain.Task{ID: "t1"}); err != nil {
		t.Fatalf("index empty task: %v", err)
	}
	results, err := s.Search(ctx, "anything", domain.SearchOptions{})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(results) != 0 {
		t.Errorf("got %d results for an empty index, want 0", len(results))
	}
}

func TestEmptyQueryReturnsNothing(t *testing.T) {
	ctx := context.Background()
	s, _ := openStore(t, &fakeEmbedder{dim: 8})

	if err := s.IndexTask(ctx, newTask("t1", "alpha", "")); err != nil {
		t.Fatalf("index: %v", err)
	}
	results, err := s.Search(ctx, "   ", domain.SearchOptions{})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(results) != 0 {
		t.Errorf("got %d results for an empty query, want 0", len(results))
	}
}

// --- Events ------------------------------------------------------------------

func TestIndexEventOnlyAgentResult(t *testing.T) {
	ctx := context.Background()
	s, _ := openStore(t, &fakeEmbedder{dim: 8})

	// Any other event type is a contract error.
	if err := s.IndexEvent(ctx, domain.Event{ID: "e1", Type: domain.EventTaskCreated}); err == nil {
		t.Error("IndexEvent with a non-agent.result event must fail")
	}

	// agent.result with a response is indexed and searchable as agent memory.
	ev := domain.Event{
		ID:   "e1",
		Type: domain.EventAgentResult,
		Payload: map[string]any{
			"task_id":  "t1",
			"response": "the dentist appointment is booked",
		},
	}
	if err := s.IndexEvent(ctx, ev); err != nil {
		t.Fatalf("index event: %v", err)
	}
	results, err := s.Search(ctx, "dentist appointment booked", domain.SearchOptions{Limit: 5})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("got %d results, want 1", len(results))
	}
	if results[0].Ref != (domain.Ref{Kind: domain.RefKindEvent, ID: "e1"}) {
		t.Errorf("ref = %+v, want event e1", results[0].Ref)
	}
	if results[0].Text != "the dentist appointment is booked" {
		t.Errorf("text = %q, want the agent response", results[0].Text)
	}
}

func TestIndexEventEmptyResponseSkipped(t *testing.T) {
	ctx := context.Background()
	s, _ := openStore(t, &fakeEmbedder{dim: 8})
	if err := s.IndexEvent(ctx, domain.Event{ID: "e1", Type: domain.EventAgentResult, Payload: map[string]any{}}); err != nil {
		t.Fatalf("index empty event: %v", err)
	}
	results, err := s.Search(ctx, "anything", domain.SearchOptions{})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(results) != 0 {
		t.Errorf("got %d results, want 0", len(results))
	}
}

// --- Reset / rebuild ---------------------------------------------------------

func TestResetClearsOrphans(t *testing.T) {
	ctx := context.Background()
	s, dbPath := openStore(t, &fakeEmbedder{dim: 8})

	if err := s.IndexTask(ctx, newTask("t1", "alpha", "")); err != nil {
		t.Fatalf("index t1: %v", err)
	}
	if err := s.IndexTask(ctx, newTask("t2", "beta", "")); err != nil {
		t.Fatalf("index t2: %v", err)
	}

	// Simulate an orphan left by a failed RemoveTask: insert a row directly.
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open raw db: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO search_docs (kind, ref_id, text, embedding) VALUES ('task','ghost','haunted',x'00000000')`); err != nil {
		t.Fatalf("insert orphan: %v", err)
	}
	db.Close()
	if n := rawRows(t, dbPath); n != 3 {
		t.Fatalf("rows before reset = %d, want 3", n)
	}

	// Reset wipes everything, orphans included (idempotent: twice is fine).
	if err := s.Reset(ctx); err != nil {
		t.Fatalf("reset: %v", err)
	}
	if err := s.Reset(ctx); err != nil {
		t.Fatalf("reset again: %v", err)
	}
	if n := rawRows(t, dbPath); n != 0 {
		t.Errorf("rows after reset = %d, want 0", n)
	}
	results, err := s.Search(ctx, "alpha", domain.SearchOptions{})
	if err != nil {
		t.Fatalf("search after reset: %v", err)
	}
	if len(results) != 0 {
		t.Errorf("got %d results after reset, want 0", len(results))
	}
}

// --- Persistence -------------------------------------------------------------

func TestDerivedIndexPersistsAcrossReopen(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "reopen.db")

	st1, err := sqlite.Open(dbPath)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := st1.NewSemanticStore(&fakeEmbedder{dim: 8}).IndexTask(ctx, newTask("t1", "alpha", "")); err != nil {
		t.Fatalf("index: %v", err)
	}
	if err := st1.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	st2, err := sqlite.Open(dbPath)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer st2.Close()
	s2 := st2.NewSemanticStore(&fakeEmbedder{dim: 8})

	results, err := s2.Search(ctx, "alpha", domain.SearchOptions{})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(results) != 1 || results[0].Ref.ID != "t1" {
		t.Errorf("derived index did not survive reopen: %+v", results)
	}
}
