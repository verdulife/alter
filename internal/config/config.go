package config

import (
	"os"
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

	// PiBridgeEnabled opts the inbound free-text route into the direct
	// Telegram→Pi bridge (internal/bridge): one persistent pi process with
	// session persistence and a clean load (no extensions, skills, prompt
	// templates, themes or context files; only the read-only tool allowlist by
	// default). When true it takes precedence over the AgentFlow free-text route
	// for inbound messages only; the Scheduler action is untouched. Off by
	// default.
	PiBridgeEnabled bool
	// BridgeSessionName is the pi session display name for the persistent bridge
	// process (--name), used to identify the saved session.
	BridgeSessionName string
	// BridgeTools is the comma-separated allowlist of tools for the bridge pi
	// process (--tools). It is a STRICT allowlist in pi: only the listed tools
	// stay active (read, grep, find, ls by default), so system tools such as
	// bash, edit and write are off. Empty keeps the legacy --no-tools behavior.
	BridgeTools string
	// BridgePromptFiles are persona/system-prompt markdown files appended to the
	// bridge pi process via --append-system-prompt, one flag per file. Paths are
	// semicolon-separated in ALTER_BRIDGE_PROMPT_FILES; pi resolves each path
	// and reads the file contents into the system prompt.
	BridgePromptFiles []string

	// BridgeWebTools, when true, appends the pi-web-access web tools
	// (web_search, fetch_content, get_search_content) to the bridge allowlist
	// (ALTER_BRIDGE_TOOLS) and expects a pi-web-access extension to be loaded
	// via BridgeExtensions. Off by default (D-W): web access is an outbound
	// network effect and cost on the host provider account, not read-only local
	// access, so it must be opted in explicitly.
	BridgeWebTools bool
	// BridgeExtensions are explicit extension files loaded by the bridge pi
	// process with -e, one flag per path (ALTER_BRIDGE_EXTENSIONS). Each loads
	// only the named extension (pi-web-access) and works even while
	// --no-extensions stays active (explicit -e paths win), so the bridge keeps
	// its clean load and never auto-discovers the host's packages.
	BridgeExtensions []string

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
	// defaultBridgeSessionName is used to name the persistent bridge pi session.
	defaultBridgeSessionName = "alter-bridge"
	// defaultBridgeTools is the read-only tool allowlist for the bridge pi
	// process; system tools (bash, edit, write) are intentionally not listed.
	defaultBridgeTools = "read,grep,find,ls"
	// bridgeWebTools are appended to the allowlist when BridgeWebTools is on
	// (see §12 of docs/bridge-telegram-pi.md). They come from the pi-web-access
	// extension and only make sense with it loaded via BridgeExtensions. This
	// is the safe, curated subset: search + fetch (and the lookup back into
	// stored results); source_check is intentionally excluded for M2-W.
	bridgeWebTools = ",web_search,fetch_content,get_search_content"
	// defaultSearchLimit caps semantic search results when ALTER_SEARCH_LIMIT
	// is unset (0 in SearchOptions means "adapter default").
	defaultSearchLimit = 5
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

		PiBridgeEnabled:    parseBoolEnv("ALTER_PI_BRIDGE", false),
		BridgeSessionName:  envOr("ALTER_BRIDGE_SESSION_NAME", defaultBridgeSessionName),
		BridgeTools:        envOr("ALTER_BRIDGE_TOOLS", defaultBridgeTools),
		BridgeWebTools:     parseBoolEnv("ALTER_BRIDGE_WEB_TOOLS", false),
		BridgeExtensions:   parseListEnv("ALTER_BRIDGE_EXTENSIONS"),
		BridgePromptFiles:  parseListEnv("ALTER_BRIDGE_PROMPT_FILES"),

		Timezone: envOr("ALTER_TIMEZONE", ""),

		// Semantic search: off by default; provider/URL optional and unused until
		// a provider is implemented.
		SearchEnabled:     parseBoolEnv("ALTER_SEARCH_ENABLED", false),
		EmbeddingProvider: envOr("ALTER_EMBEDDING_PROVIDER", ""),
		EmbeddingURL:      envOr("ALTER_EMBEDDING_URL", ""),
		SearchLimit:       parseIntEnv("ALTER_SEARCH_LIMIT", defaultSearchLimit),
	}
}

// envOr returns the environment variable value or a default when empty.
func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// parseListEnv splits a semicolon-separated environment variable into a list,
// returning nil when unset or empty.
func parseListEnv(key string) []string {
	v := os.Getenv(key)
	if v == "" {
		return nil
	}
	return strings.Split(v, ";")
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
