package config

import (
	"os"
	"strconv"
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
}

// Load returns the application configuration from environment variables
// (env-only; there is no config file in this phase). Callers may override the
// returned values with command-line flags after Load.
func Load() Config {
	return Config{
		Version:        "0.1.0",
		DBPath:         envOr("ALTER_DB_PATH", "alter.db"),
		TelegramToken:  envOr("TELEGRAM_TOKEN", ""),
		TelegramChatID: parseInt64Env("TELEGRAM_CHAT_ID", 0),
	}
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
