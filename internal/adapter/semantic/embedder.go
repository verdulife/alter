// Package semantic implements the semantic search capability of alter as a
// provider-independent port/adapters pair: the domain ports (domain.SemanticSearcher /
// domain.SemanticIndexer) over a SQLite-backed vector store, plus an Embedder
// seam so the concrete embedding provider can be omitted or replaced without
// touching domain, services or the runtime.
//
// This slice deliberately ships NO embedding provider: NewEmbedder returns a nil
// Embedder when none is configured, and a Store built with a nil Embedder makes
// every index/search operation degrade to domain.ErrSemanticUnavailable
// (fail-open). A future provider (e.g. Ollama, an OpenAI-compatible HTTP API)
// implements Embedder and is selected via ALTER_EMBEDDING_PROVIDER.
package semantic

import (
	"context"
	"fmt"
)

// Embedder turns texts into dense vectors. It is intentionally neither a domain
// port (it is a mechanism, not a business contract) nor bound to any provider.
// Implementations must embed all inputs or return an error (never partial
// results); the vector Store normalizes the returned vectors itself, so
// providers return whatever the model produces.
type Embedder interface {
	// Embed embeds texts as a batch and returns one vector per input, in
	// order. All vectors of one call must share the same dimension.
	Embed(ctx context.Context, texts []string) ([][]float32, error)
	// Name identifies the provider for diagnostics (e.g. "ollama"). It is
	// never used for routing; it exists so the runtime can log which provider
	// serves the index.
	Name() string
}

// EmbedderConfig selects an embedding provider. Empty Provider means "none
// configured": the runtime runs with semantic search degraded to
// domain.ErrSemanticUnavailable (fail-open), never as a hard dependency.
type EmbedderConfig struct {
	// Provider selects the embedding provider. No provider is implemented in
	// this slice; a future one is selected here.
	Provider string
	// BaseURL is the optional service endpoint of the provider. Unused by the
	// (absent) V1 providers.
	BaseURL string
}

// NewEmbedder returns the Embedder selected by cfg, or (nil, nil) when no
// provider is configured. A non-empty unknown Provider is a configuration
// error: the runtime must log it and continue without search (never fail).
func NewEmbedder(cfg EmbedderConfig) (Embedder, error) {
	switch cfg.Provider {
	case "":
		return nil, nil
	default:
		return nil, fmt.Errorf("semantic: unsupported embedding provider %q (this slice ships no provider yet)", cfg.Provider)
	}
}
