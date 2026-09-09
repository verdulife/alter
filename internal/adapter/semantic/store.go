package semantic

import (
	"context"
	"database/sql"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"

	"github.com/verdu/alter/internal/domain"
)

// defaultSearchLimit is applied when neither SearchOptions.Limit nor
// WithDefaultLimit configured a limit.
const defaultSearchLimit = 5

// Store is the SQLite-backed semantic search adapter: it implements
// domain.SemanticSearcher and domain.SemanticIndexer over the derived
// search_docs table (see migrations/0004_semantic_index.sql).
//
// The index is a derived projection: SQLite tables tasks/events stay the source
// of truth, and the whole index can be wiped (Reset) and rebuilt from them at
// any time. Embedding vectors are stored normalized as little-endian float32
// blobs; search is an exact (brute-force) cosine scan over all rows, which is
// appropriate for the V1 corpus (hundreds to low thousands of docs at
// dimension ~384): no ANN index or external vector DB is introduced in this
// phase.
//
// A Store built with a nil Embedder serves every index/search operation with
// domain.ErrSemanticUnavailable, so callers fail open instead of blocking.
type Store struct {
	db       *sql.DB
	embedder Embedder
	// defaultLimit is applied when SearchOptions.Limit == 0 (always >= 1).
	defaultLimit int
	// dim is the expected embedding dimension, learned from the first
	// successful embed and validated afterwards to catch provider drift.
	dim int
}

var (
	_ domain.SemanticSearcher = (*Store)(nil)
	_ domain.SemanticIndexer  = (*Store)(nil)
)

// Option configures a Store.
type Option func(*Store)

// WithDefaultLimit sets the limit applied when SearchOptions.Limit is zero.
// A non-positive value keeps the built-in default (5).
func WithDefaultLimit(n int) Option {
	return func(s *Store) {
		if n > 0 {
			s.defaultLimit = n
		}
	}
}

// NewStore builds a Store over the given SQLite connection. embedder may be
// nil (every index/search operation then degrades to
// domain.ErrSemanticUnavailable; Reset still works).
func NewStore(db *sql.DB, embedder Embedder, opts ...Option) *Store {
	s := &Store{db: db, embedder: embedder, defaultLimit: defaultSearchLimit}
	for _, o := range opts {
		o(s)
	}
	return s
}

// Reset wipes the whole derived index. It is idempotent and needs no Embedder.
func (s *Store) Reset(ctx context.Context) error {
	if _, err := s.db.ExecContext(ctx, `DELETE FROM search_docs`); err != nil {
		return fmt.Errorf("semantic: reset index: %w", err)
	}
	return nil
}

// IndexTask upserts the derived text projection of a task.
func (s *Store) IndexTask(ctx context.Context, task domain.Task) error {
	text := taskIndexText(task)
	if strings.TrimSpace(text) == "" {
		// Nothing indexable (defensive: a task always has a title in V1).
		return nil
	}
	vec, err := s.embed(ctx, text)
	if err != nil {
		return err
	}
	return s.upsert(ctx, string(domain.RefKindTask), task.ID, text, vec)
}

// RemoveTask deletes the task's index entry. Best-effort by contract: a
// failure leaves an orphan that the next Reset+rebuild removes.
func (s *Store) RemoveTask(ctx context.Context, taskID string) error {
	return s.deleteRow(ctx, string(domain.RefKindTask), taskID)
}

// IndexEvent upserts an agent.result Event (its response payload) as
// retrievable agent memory. Any other event type is an error.
func (s *Store) IndexEvent(ctx context.Context, event domain.Event) error {
	if event.Type != domain.EventAgentResult {
		return fmt.Errorf("semantic: IndexEvent only indexes %q events, got %q", domain.EventAgentResult, event.Type)
	}
	response, ok := event.Payload["response"].(string)
	if !ok || strings.TrimSpace(response) == "" {
		// No retrievable text: nothing to index (defensive).
		return nil
	}
	vec, err := s.embed(ctx, response)
	if err != nil {
		return err
	}
	return s.upsert(ctx, string(domain.RefKindEvent), event.ID, response, vec)
}

// Search returns the most relevant indexed entities for query, ranked by
// decreasing cosine similarity, excluding opts.Exclude and capped by
// opts.Limit (0 → the configured default). It never returns an error that
// should block the caller's flow: failures are fail-open at every caller.
func (s *Store) Search(ctx context.Context, query string, opts domain.SearchOptions) ([]domain.SearchResult, error) {
	if s.embedder == nil {
		return nil, fmt.Errorf("%w: no embedding provider configured", domain.ErrSemanticUnavailable)
	}
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, nil
	}
	qvec, err := s.embed(ctx, query)
	if err != nil {
		return nil, err
	}

	exclude := make(map[[2]string]bool, len(opts.Exclude))
	for _, r := range opts.Exclude {
		exclude[[2]string{string(r.Kind), r.ID}] = true
	}

	rows, err := s.db.QueryContext(ctx, `SELECT kind, ref_id, text, embedding FROM search_docs`)
	if err != nil {
		return nil, fmt.Errorf("semantic: query index: %w", err)
	}
	defer rows.Close()

	type hit struct {
		ref   domain.Ref
		text  string
		score float64
	}
	var hits []hit
	for rows.Next() {
		var kind, refID, text string
		var blob []byte
		if err := rows.Scan(&kind, &refID, &text, &blob); err != nil {
			return nil, fmt.Errorf("semantic: scan index row: %w", err)
		}
		if exclude[[2]string{kind, refID}] {
			continue
		}
		vec, err := decodeEmbedding(blob)
		if err != nil {
			return nil, err
		}
		if len(vec) != len(qvec) {
			// Dimension drift (provider/model changed mid-life): skip
			// incompatible docs instead of corrupting the ranking.
			continue
		}
		hits = append(hits, hit{
			ref:   domain.Ref{Kind: domain.RefKind(kind), ID: refID},
			text:  text,
			score: dot(qvec, vec),
		})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("semantic: iterate index: %w", err)
	}

	sort.SliceStable(hits, func(i, j int) bool { return hits[i].score > hits[j].score })
	if len(hits) > s.defaultLimit && opts.Limit == 0 && s.defaultLimit > 0 {
		hits = hits[:s.defaultLimit]
	} else if opts.Limit > 0 && len(hits) > opts.Limit {
		hits = hits[:opts.Limit]
	}

	out := make([]domain.SearchResult, 0, len(hits))
	for _, h := range hits {
		out = append(out, domain.SearchResult{Ref: h.ref, Text: h.text, Score: h.score})
	}
	return out, nil
}

// embed embeds one text via the configured provider, normalizes it and keeps
// the embedding dimension consistent.
func (s *Store) embed(ctx context.Context, text string) ([]float32, error) {
	if s.embedder == nil {
		return nil, fmt.Errorf("%w: no embedding provider configured", domain.ErrSemanticUnavailable)
	}
	vecs, err := s.embedder.Embed(ctx, []string{text})
	if err != nil {
		return nil, fmt.Errorf("semantic: embed: %w", err)
	}
	if len(vecs) != 1 {
		return nil, fmt.Errorf("semantic: embedder returned %d vectors for 1 input", len(vecs))
	}
	vec := vecs[0]
	if s.dim == 0 {
		s.dim = len(vec)
	} else if len(vec) != s.dim {
		return nil, fmt.Errorf("semantic: embedding dimension drift: got %d, expected %d (provider/model changed?)", len(vec), s.dim)
	}
	return normalize(vec), nil
}

// upsert inserts or replaces one derived index row.
func (s *Store) upsert(ctx context.Context, kind, refID, text string, vec []float32) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO search_docs (kind, ref_id, text, embedding) VALUES (?,?,?,?)
		ON CONFLICT(kind, ref_id) DO UPDATE SET text = excluded.text, embedding = excluded.embedding`,
		kind, refID, text, encodeEmbedding(vec))
	if err != nil {
		return fmt.Errorf("semantic: upsert %s/%s: %w", kind, refID, err)
	}
	return nil
}

// deleteRow removes one derived index row (no-op when absent).
func (s *Store) deleteRow(ctx context.Context, kind, refID string) error {
	if _, err := s.db.ExecContext(ctx, `DELETE FROM search_docs WHERE kind = ? AND ref_id = ?`, kind, refID); err != nil {
		return fmt.Errorf("semantic: delete %s/%s: %w", kind, refID, err)
	}
	return nil
}

// taskIndexText is the indexable text projection of a task: title, description
// and source, newline-separated. It is a projection owned by the search
// adapter: the domain Task is never modified for search, and Scheduler/Agent
// internals are never indexed (scheduling bookkeeping has no semantic value).
func taskIndexText(t domain.Task) string {
	return strings.Join([]string{t.Title, t.Description, t.Source}, "\n")
}

// --- Vector encoding helpers (float32 LE blobs) -------------------------------

func encodeEmbedding(v []float32) []byte {
	b := make([]byte, 4*len(v))
	for i, f := range v {
		binary.LittleEndian.PutUint32(b[4*i:], math.Float32bits(f))
	}
	return b
}

func decodeEmbedding(b []byte) ([]float32, error) {
	if len(b)%4 != 0 {
		return nil, errors.New("semantic: malformed embedding blob")
	}
	v := make([]float32, len(b)/4)
	for i := range v {
		v[i] = math.Float32frombits(binary.LittleEndian.Uint32(b[4*i:]))
	}
	return v, nil
}

// normalize returns a unit-length copy of v (zero vectors stay zero).
func normalize(v []float32) []float32 {
	var sum float64
	for _, f := range v {
		sum += float64(f) * float64(f)
	}
	if sum == 0 {
		return append([]float32(nil), v...)
	}
	inv := float32(1 / math.Sqrt(sum))
	out := make([]float32, len(v))
	for i, f := range v {
		out[i] = f * inv
	}
	return out
}

// dot is the cosine similarity of two unit vectors.
func dot(a, b []float32) float64 {
	var s float64
	for i := range a {
		s += float64(a[i]) * float64(b[i])
	}
	return s
}
