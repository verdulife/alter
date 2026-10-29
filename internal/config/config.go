package config

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Config holds application-wide configuration.
//
// Every field here belongs to the one runtime the binary still has: the
// Telegram ↔ Pi bridge plus the vault. There is no database, no scheduler and no
// second entry path, so there is no configuration for them either.
type Config struct {
	Version string
	// TelegramToken is the Bot API token used to poll and send messages.
	TelegramToken string
	// TelegramChatID is the numeric owner chat: the only chat the poller accepts
	// and the only destination for every reply, attachment and button.
	TelegramChatID int64

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
	// TelegramAPIBase is the Bot API base URL, without a trailing slash.
	// Defaults to the public cloud API. Setting it to a self-hosted
	// telegram-bot-api server (e.g. http://127.0.0.1:8081) unlocks uploads up to
	// 2000 MB, downloads without a limit and file:// uploads, with no code change.
	TelegramAPIBase string

	// VaultDir is the global notes vault: the store the deterministic bridge tools
	// (and this server) write through. Defaults to $HOME/vault. It is data, never a
	// boot dependency: a missing directory is created lazily on first write. It is
	// NOT the working directory of Pi; that is WorkDir.
	VaultDir string
	// WorkDir is the working directory of the bridge's Pi process (cwd). Defaults
	// to $HOME, so Pi sees the same tree a terminal session sees: the bridge is the
	// owner's gentle-shell, not a sandbox with a smaller view.
	WorkDir string

	// BridgeSessionID is the Pi session resumed across messages, so the
	// conversation keeps its thread. Defaults to "telegram".
	BridgeSessionID string
	// BridgeTools is the optional Pi tool allowlist passed via --tools. Empty, the
	// default, means no restriction: the flag is not passed and Pi keeps its own
	// full selection from the global configuration, so `bash`, `edit` and `write`
	// are available like in a terminal session (T11). Setting it narrows the
	// selection on purpose; it never extends Pi's defaults.
	BridgeTools string
	// BridgePromptFiles are markdown files appended to the system prompt via
	// --append-system-prompt, in order. Defaults to the repository-relative
	// alter-persona.md, so a bridge started from the repository root needs no
	// extra configuration.
	BridgePromptFiles []string
	// BridgeExtensions are explicit extension entry points loaded with -e. They are
	// additive: extension discovery stays enabled, so the global packages load too.
	// Defaults to the repository-relative tools extension, which is the only
	// thing that gives Pi its deterministic write tools.
	BridgeExtensions []string
	// BridgeTimeout bounds one full bridge turn (spawn + model + tools + reply).
	BridgeTimeout time.Duration
}

// Defaults for the bridge configuration.
const (
	defaultPiBin = "pi"

	defaultTelegramAPIBase = "https://api.telegram.org"
	defaultBridgeSessionID = "telegram"
	// defaultBridgeTools is deliberately empty: the bridge passes no --tools when
	// ALTER_BRIDGE_TOOLS is unset, so Pi keeps its own full tool selection from the
	// global configuration instead of a forced allowlist (T11).
	defaultBridgeTools = ""
	// defaultBridgeTimeout bounds one turn. Bash turns are long, so the ceiling is
	// generous; ALTER_BRIDGE_TIMEOUT lowers or raises it.
	defaultBridgeTimeout = 300 * time.Second

	// defaultBridgePromptFiles and defaultBridgeExtensions are the assets the
	// phase-1 bridge needs from Pi, both repository-relative so that running the
	// binary from the repository root works with no environment at all. Missing
	// them is a silent failure (Pi would write nothing), so the wiring verifies
	// them at start-up instead of trusting the defaults.
	defaultBridgePromptFiles = "alter-persona.md"
	defaultBridgeExtensions  = "internal/tools/extension/index.ts"
)

// Load returns the application configuration from environment variables
// (env-only; there is no config file in this phase). Callers may override the
// returned values with command-line flags after Load.
func Load() Config {
	return Config{
		Version:        "0.1.0",
		TelegramToken:  envOr("TELEGRAM_TOKEN", ""),
		TelegramChatID: parseInt64Env("TELEGRAM_CHAT_ID", 0),

		// Provider/model are optional overrides; pi falls back to its own
		// configuration when empty.
		PiBin:      envOr("ALTER_PI_BIN", defaultPiBin),
		PiProvider: envOr("ALTER_PI_PROVIDER", ""),
		PiModel:    envOr("ALTER_PI_MODEL", ""),

		TelegramAPIBase: normalizeBaseURL(envOr("ALTER_TELEGRAM_API_BASE", defaultTelegramAPIBase)),
		VaultDir:        envOr("ALTER_VAULT_DIR", defaultVaultDir()),
		WorkDir:         envOr("ALTER_BRIDGE_WORKDIR", defaultWorkDir()),

		BridgeSessionID:   envOr("ALTER_BRIDGE_SESSION_ID", defaultBridgeSessionID),
		BridgeTools:       envOr("ALTER_BRIDGE_TOOLS", defaultBridgeTools),
		BridgePromptFiles: splitListEnv("ALTER_BRIDGE_PROMPT_FILES", defaultBridgePromptFiles),
		BridgeExtensions:  splitListEnv("ALTER_BRIDGE_EXTENSIONS", defaultBridgeExtensions),
		BridgeTimeout:     parseDurationEnv("ALTER_BRIDGE_TIMEOUT", defaultBridgeTimeout),
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

// defaultWorkDir returns $HOME, the directory Pi works in: the bridge is the
// owner's shell, so it must see what a terminal session sees. ALTER_BRIDGE_WORKDIR
// always wins; without a resolvable home the bridge inherits its own working
// directory instead of inventing a path.
func defaultWorkDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return home
}

// splitListEnv parses a semicolon-separated list, trimming whitespace and
// dropping empty entries. def is the semicolon-separated default list, used when
// the variable is unset or yields no entry at all, so an empty value never
// silently drops an asset the bridge depends on.
func splitListEnv(key, def string) []string {
	if list := parseList(os.Getenv(key)); len(list) > 0 {
		return list
	}
	return parseList(def)
}

// parseList parses a semicolon-separated list, trimming whitespace and dropping
// empty entries. An empty value yields nil.
func parseList(raw string) []string {
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
