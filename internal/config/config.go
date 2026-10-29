package config

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Config holds application-wide configuration.
type Config struct {
	Version string
	// DBPath is the path to the SQLite database file (the source of truth in
	// this phase).
	DBPath string
	// TelegramToken is the Bot API token used to poll and send messages.
	TelegramToken string
	// TelegramChatID is the numeric owner chat that receives application-initiated
	// notifications via domain.Channel. It is fixed at startup; command replies are
	// sent directly to the originating chat by the inbound adapter and never pass
	// through Channel.
	TelegramChatID int64

	// PiEnabled opts the runtime into the real Pi Agent: when true, the Scheduler
	// action is the agent.Orchestrator (Agent -> Channel -> Event best-effort)
	// driven by a Pi RPC adapter; when false, the runtime keeps the Telegram-only
	// NotifyAction. Pi is never used unless explicitly enabled.
	PiEnabled bool
	// PiBin is the pi CLI binary path. Defaults to "pi" resolved via PATH; a
	// shell-independent deployment should set an absolute path because the PATH
	// seen by services (systemd/cron) rarely includes the fnm multishell where pi
	// is installed interactively.
	PiBin string
	// PiProvider optionally overrides the LLM provider passed to pi (--provider).
	// Empty lets pi use its own default/configured provider.
	PiProvider string
	// PiModel optionally overrides the model passed to pi (--model, "provider/id[:thinking]").
	// Empty lets pi use its own configured/default model.
	PiModel string
	// PiTimeout bounds one Agent execution (spawn + LLM run + response).
	PiTimeout time.Duration
	// PiNoTools disables all agent tools (--no-tools): the agent only produces
	// user-facing text and cannot perform side effects. Default true for V1
	// safety/cost; set false to re-enable tools explicitly.
	PiNoTools bool
	// PiSystemPrompt is an optional extra system prompt appended via --append-system-prompt.
	PiSystemPrompt string

	// TelegramAPIBase is the Bot API base URL, without a trailing slash.
	// Defaults to the public cloud API. Setting it to a self-hosted
	// telegram-bot-api server (e.g. http://127.0.0.1:8081) unlocks uploads up to
	// 2000 MB, downloads without a limit and file:// uploads, with no code change.
	TelegramAPIBase string

	// VaultDir is the global notes vault served to Pi as its working directory.
	// Defaults to $HOME/vault. It is data, never a boot dependency: a missing
	// directory is created lazily on first write.
	VaultDir string

	// BridgeEnabled opts the runtime into the Telegram <-> Pi bridge as the only
	// free-text entry path.
	BridgeEnabled bool
	// BridgeSessionID is the Pi session resumed across messages, so the
	// conversation keeps its thread. Defaults to "telegram".
	BridgeSessionID string
	// BridgeTools is the Pi tool allowlist passed via --tools. Pi never gets
	// bash/edit/write: the vault is read with read/grep/find/ls and mutated only
	// through deterministic extension tools.
	BridgeTools string
	// BridgePromptFiles are markdown files appended to the system prompt via
	// --append-system-prompt, in order.
	BridgePromptFiles []string
	// BridgeExtensions are explicit extension entry points loaded with -e. They
	// coexist with --no-extensions (discovery off, explicit paths still load).
	BridgeExtensions []string
	// BridgeTimeout bounds one full bridge turn (spawn + model + tools + reply).
	BridgeTimeout time.Duration

	// Timezone is the user's timezone for resolving time expressions.
	// Defaults to the system timezone (time.Local).
	Timezone string

	// Semantic search: a derived vector index over tasks and agent.result events
	// that supplies retrievable context to the Agent at fire time. SQLite stays
	// the source of truth; search_docs is a derived, rebuildable projection.
	// Disabled by default; requires an embedding provider (none is shipped in
	// this phase) and must never become a boot dependency of the runtime.
	SearchEnabled     bool
	EmbeddingProvider string
	EmbeddingURL      string
	SearchLimit       int
}

// Defaults for the optional Pi Agent configuration.
const (
	defaultPiBin     = "pi"
	defaultPiTimeout = 60 * time.Second
	// defaultSearchLimit caps semantic search results when ALTER_SEARCH_LIMIT
	// is unset (0 in SearchOptions means "adapter default").
	defaultSearchLimit = 5

	defaultTelegramAPIBase = "https://api.telegram.org"
	defaultBridgeSessionID = "telegram"
	// defaultBridgeTools is the Pi tool allowlist used when ALTER_BRIDGE_TOOLS is
	// unset. --tools REPLACES Pi's whole selection instead of extending it, so the
	// deterministic bridge tools must be listed here explicitly: a default with
	// only the read-only tools would silently disable every vault write (S7).
	defaultBridgeTools   = "read,grep,find,ls,save_note,append_note,send_file,ask_options"
	defaultBridgeTimeout = 120 * time.Second
)

// Load returns the application configuration from environment variables
// (env-only; there is no config file in this phase). Callers may override the
// returned values with command-line flags after Load.
func Load() Config {
	return Config{
		Version:        "0.1.0",
		DBPath:         envOr("ALTER_DB_PATH", "alter.db"),
		TelegramToken:  envOr("TELEGRAM_TOKEN", ""),
		TelegramChatID: parseInt64Env("TELEGRAM_CHAT_ID", 0),

		// Pi Agent: off unless explicitly enabled. Provider/model are optional
		// overrides; pi falls back to its own configuration when empty.
		PiEnabled:      parseBoolEnv("ALTER_PI_ENABLED", false),
		PiBin:          envOr("ALTER_PI_BIN", defaultPiBin),
		PiProvider:     envOr("ALTER_PI_PROVIDER", ""),
		PiModel:        envOr("ALTER_PI_MODEL", ""),
		PiTimeout:      parseDurationEnv("ALTER_PI_TIMEOUT", defaultPiTimeout),
		PiNoTools:      parseBoolEnv("ALTER_PI_NO_TOOLS", true),
		PiSystemPrompt: envOr("ALTER_PI_SYSTEM_PROMPT", ""),

		TelegramAPIBase: normalizeBaseURL(envOr("ALTER_TELEGRAM_API_BASE", defaultTelegramAPIBase)),
		VaultDir:        envOr("ALTER_VAULT_DIR", defaultVaultDir()),

		BridgeEnabled:     parseBoolEnv("ALTER_BRIDGE_ENABLED", false),
		BridgeSessionID:   envOr("ALTER_BRIDGE_SESSION_ID", defaultBridgeSessionID),
		BridgeTools:       envOr("ALTER_BRIDGE_TOOLS", defaultBridgeTools),
		BridgePromptFiles: splitListEnv("ALTER_BRIDGE_PROMPT_FILES"),
		BridgeExtensions:  splitListEnv("ALTER_BRIDGE_EXTENSIONS"),
		BridgeTimeout:     parseDurationEnv("ALTER_BRIDGE_TIMEOUT", defaultBridgeTimeout),

		Timezone: envOr("ALTER_TIMEZONE", ""),

		// Semantic search: off by default; provider/URL optional and unused until
		// a provider is implemented.
		SearchEnabled:     parseBoolEnv("ALTER_SEARCH_ENABLED", false),
		EmbeddingProvider: envOr("ALTER_EMBEDDING_PROVIDER", ""),
		EmbeddingURL:      envOr("ALTER_EMBEDDING_URL", ""),
		SearchLimit:       parseIntEnv("ALTER_SEARCH_LIMIT", defaultSearchLimit),
	}
}

// normalizeBaseURL removes trailing slashes so path joins stay predictable and
// the self-hosted Bot API server (http://host:port) works the same as the cloud
// API. An empty value falls back to the public API.
func normalizeBaseURL(v string) string {
	v = strings.TrimRight(strings.TrimSpace(v), "/")
	if v == "" {
		return defaultTelegramAPIBase
	}
	return v
}

// defaultVaultDir returns $HOME/vault, which is where this server keeps its
// notes. It is a default only: ALTER_VAULT_DIR always wins.
func defaultVaultDir() string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return "vault"
	}
	return filepath.Join(home, "vault")
}

// splitListEnv parses a semicolon-separated list, trimming whitespace and
// dropping empty entries. An unset variable yields nil.
func splitListEnv(key string) []string {
	raw := os.Getenv(key)
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	parts := strings.Split(raw, ";")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// envOr returns the environment variable value or a default when empty.
func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// parseInt64Env parses an integer environment variable, falling back to def when
// unset or malformed.
func parseInt64Env(key string, def int64) int64 {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		return def
	}
	return n
}

// parseBoolEnv parses a boolean environment variable, falling back to def when
// unset or malformed.
func parseBoolEnv(key string, def bool) bool {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return def
	}
	return b
}

// parseIntEnv parses an integer environment variable, falling back to def when
// unset or malformed.
func parseIntEnv(key string, def int) int {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return def
	}
	return n
}

// parseDurationEnv parses a Go duration environment variable, falling back to
// def when unset or malformed.
func parseDurationEnv(key string, def time.Duration) time.Duration {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return def
	}
	return d
}
