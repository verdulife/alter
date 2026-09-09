-- 0004_semantic_index.sql
-- Derived semantic search index (dense vector embeddings).
--
-- search_docs is a DERIVED projection of the SQLite source of truth (tasks and
-- agent.result events): it is never authoritative. It can be wiped (Reset) and
-- fully rebuilt from those tables at any time; a failed RemoveTask or index
-- write may leave an orphan here, and the next boot's Reset + reindex
-- converges. Embeddings are stored as normalized float32 little-endian blobs;
-- search is an exact per-row cosine scan (appropriate for the V1 corpus).

CREATE TABLE IF NOT EXISTS search_docs (
    kind      TEXT NOT NULL,
    ref_id    TEXT NOT NULL,
    text      TEXT NOT NULL,
    embedding BLOB NOT NULL,
    PRIMARY KEY (kind, ref_id)
);