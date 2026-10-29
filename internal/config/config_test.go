package config

import (
	"path/filepath"
	"testing"
	"time"
)

func TestLoadDefaults(t *testing.T) {
	t.Setenv("ALTER_DB_PATH", "")
	t.Setenv("TELEGRAM_TOKEN", "")
	t.Setenv("TELEGRAM_CHAT_ID", "")
	t.Setenv("ALTER_PI_ENABLED", "")
	t.Setenv("ALTER_PI_BIN", "")
	t.Setenv("ALTER_PI_PROVIDER", "")
	t.Setenv("ALTER_PI_MODEL", "")
	t.Setenv("ALTER_PI_TIMEOUT", "")
	t.Setenv("ALTER_PI_NO_TOOLS", "")
	t.Setenv("ALTER_PI_SYSTEM_PROMPT", "")
	t.Setenv("ALTER_SEARCH_ENABLED", "")
	t.Setenv("ALTER_EMBEDDING_PROVIDER", "")
	t.Setenv("ALTER_EMBEDDING_URL", "")
	t.Setenv("ALTER_SEARCH_LIMIT", "")

	cfg := Load()
	if cfg.Version != "0.1.0" {
		t.Errorf("Version = %q, want 0.1.0", cfg.Version)
	}
	if cfg.DBPath != "alter.db" {
		t.Errorf("DBPath = %q, want alter.db", cfg.DBPath)
	}
	if cfg.TelegramToken != "" {
		t.Errorf("TelegramToken = %q, want empty", cfg.TelegramToken)
	}
	if cfg.TelegramChatID != 0 {
		t.Errorf("TelegramChatID = %d, want 0", cfg.TelegramChatID)
	}
	// Pi Agent defaults: disabled unless opted in, safe/restricted by default.
	if cfg.PiEnabled {
		t.Error("PiEnabled = true, want false by default")
	}
	if cfg.PiBin != "pi" {
		t.Errorf("PiBin = %q, want pi", cfg.PiBin)
	}
	if cfg.PiProvider != "" {
		t.Errorf("PiProvider = %q, want empty (pi default)", cfg.PiProvider)
	}
	if cfg.PiModel != "" {
		t.Errorf("PiModel = %q, want empty (pi default)", cfg.PiModel)
	}
	if cfg.PiTimeout != 60*time.Second {
		t.Errorf("PiTimeout = %v, want 60s", cfg.PiTimeout)
	}
	if !cfg.PiNoTools {
		t.Error("PiNoTools = false, want true by default")
	}
	if cfg.PiSystemPrompt != "" {
		t.Errorf("PiSystemPrompt = %q, want empty", cfg.PiSystemPrompt)
	}
	// Semantic search defaults: disabled, no provider, limit 5.
	if cfg.SearchEnabled {
		t.Error("SearchEnabled = true, want false by default")
	}
	if cfg.EmbeddingProvider != "" {
		t.Errorf("EmbeddingProvider = %q, want empty", cfg.EmbeddingProvider)
	}
	if cfg.EmbeddingURL != "" {
		t.Errorf("EmbeddingURL = %q, want empty", cfg.EmbeddingURL)
	}
	if cfg.SearchLimit != 5 {
		t.Errorf("SearchLimit = %d, want 5", cfg.SearchLimit)
	}
}

func TestLoadFromEnv(t *testing.T) {
	t.Setenv("ALTER_DB_PATH", "/tmp/alter.db")
	t.Setenv("TELEGRAM_TOKEN", "secret")
	t.Setenv("TELEGRAM_CHAT_ID", "123456789")

	cfg := Load()
	if cfg.DBPath != "/tmp/alter.db" {
		t.Errorf("DBPath = %q, want /tmp/alter.db", cfg.DBPath)
	}
	if cfg.TelegramToken != "secret" {
		t.Errorf("TelegramToken = %q, want secret", cfg.TelegramToken)
	}
	if cfg.TelegramChatID != 123456789 {
		t.Errorf("TelegramChatID = %d, want 123456789", cfg.TelegramChatID)
	}
}

func TestLoadMalformedChatIDFallsBack(t *testing.T) {
	t.Setenv("TELEGRAM_CHAT_ID", "not-a-number")
	cfg := Load()
	if cfg.TelegramChatID != 0 {
		t.Errorf("TelegramChatID = %d, want 0 on malformed env", cfg.TelegramChatID)
	}
}

func TestLoadPiFromEnv(t *testing.T) {
	t.Setenv("ALTER_PI_ENABLED", "true")
	t.Setenv("ALTER_PI_BIN", "/home/user/.local/bin/pi")
	t.Setenv("ALTER_PI_PROVIDER", "anthropic")
	t.Setenv("ALTER_PI_MODEL", "anthropic/claude-sonnet-4:high")
	t.Setenv("ALTER_PI_TIMEOUT", "90s")
	t.Setenv("ALTER_PI_NO_TOOLS", "false")
	t.Setenv("ALTER_PI_SYSTEM_PROMPT", "Be brief.")

	cfg := Load()
	if !cfg.PiEnabled {
		t.Error("PiEnabled = false, want true")
	}
	if cfg.PiBin != "/home/user/.local/bin/pi" {
		t.Errorf("PiBin = %q, want the configured path", cfg.PiBin)
	}
	if cfg.PiProvider != "anthropic" {
		t.Errorf("PiProvider = %q, want anthropic", cfg.PiProvider)
	}
	if cfg.PiModel != "anthropic/claude-sonnet-4:high" {
		t.Errorf("PiModel = %q, want the configured pattern", cfg.PiModel)
	}
	if cfg.PiTimeout != 90*time.Second {
		t.Errorf("PiTimeout = %v, want 90s", cfg.PiTimeout)
	}
	if cfg.PiNoTools {
		t.Error("PiNoTools = true, want false after explicit override")
	}
	if cfg.PiSystemPrompt != "Be brief." {
		t.Errorf("PiSystemPrompt = %q, want the configured prompt", cfg.PiSystemPrompt)
	}
}

func TestLoadPiMalformedFallsBack(t *testing.T) {
	t.Setenv("ALTER_PI_TIMEOUT", "not-a-duration")
	t.Setenv("ALTER_PI_NO_TOOLS", "maybe")

	cfg := Load()
	if cfg.PiTimeout != 60*time.Second {
		t.Errorf("PiTimeout = %v, want 60s on malformed env", cfg.PiTimeout)
	}
	if !cfg.PiNoTools {
		t.Error("PiNoTools = false, want true on malformed env")
	}
}

func TestLoadSearchFromEnv(t *testing.T) {
	t.Setenv("ALTER_SEARCH_ENABLED", "true")
	t.Setenv("ALTER_EMBEDDING_PROVIDER", "ollama")
	t.Setenv("ALTER_EMBEDDING_URL", "http://localhost:11434/api")
	t.Setenv("ALTER_SEARCH_LIMIT", "10")

	cfg := Load()
	if !cfg.SearchEnabled {
		t.Error("SearchEnabled = false, want true")
	}
	if cfg.EmbeddingProvider != "ollama" {
		t.Errorf("EmbeddingProvider = %q, want ollama", cfg.EmbeddingProvider)
	}
	if cfg.EmbeddingURL != "http://localhost:11434/api" {
		t.Errorf("EmbeddingURL = %q, want the configured URL", cfg.EmbeddingURL)
	}
	if cfg.SearchLimit != 10 {
		t.Errorf("SearchLimit = %d, want 10", cfg.SearchLimit)
	}
}

func TestLoadSearchMalformedFallsBack(t *testing.T) {
	t.Setenv("ALTER_SEARCH_ENABLED", "maybe")
	t.Setenv("ALTER_SEARCH_LIMIT", "lots")

	cfg := Load()
	if cfg.SearchEnabled {
		t.Error("SearchEnabled = true, want false on malformed env")
	}
	if cfg.SearchLimit != 5 {
		t.Errorf("SearchLimit = %d, want 5 on malformed env", cfg.SearchLimit)
	}
}

// TestBridgeToolsDefaultIsNoAllowlist pins T11: the bridge no longer forces a
// tool selection. An unset ALTER_BRIDGE_TOOLS yields "", and the bridge then
// omits --tools entirely so Pi keeps its own full selection from the global
// configuration.
func TestBridgeToolsDefaultIsNoAllowlist(t *testing.T) {
	clearBridgeEnv(t)

	if got := Load().BridgeTools; got != "" {
		t.Fatalf("BridgeTools = %q, want empty so --tools is never passed", got)
	}
}

// TestBridgeToolsExplicitStillWins proves the escape hatch survived: an explicit
// ALTER_BRIDGE_TOOLS is passed through untouched, and nothing adds to it.
func TestBridgeToolsExplicitStillWins(t *testing.T) {
	clearBridgeEnv(t)
	t.Setenv("ALTER_BRIDGE_TOOLS", "read,grep,find,ls")

	if got := Load().BridgeTools; got != "read,grep,find,ls" {
		t.Errorf("BridgeTools = %q, want the explicit allowlist", got)
	}
}

// TestWorkDirDefaultsToHome pins the working directory of the bridge's Pi: $HOME,
// so it sees the same tree a terminal session sees. It stays independent of the
// vault, which is only where notes live.
func TestWorkDirDefaultsToHome(t *testing.T) {
	clearBridgeEnv(t)
	home := t.TempDir()
	t.Setenv("HOME", home)

	cfg := Load()
	if cfg.WorkDir != home {
		t.Errorf("WorkDir = %q, want $HOME %q", cfg.WorkDir, home)
	}
	if want := filepath.Join(home, "vault"); cfg.VaultDir != want {
		t.Errorf("VaultDir = %q, want %q: the vault keeps its own value", cfg.VaultDir, want)
	}
}

// TestWorkDirFromEnvWins proves ALTER_BRIDGE_WORKDIR overrides $HOME without
// touching the vault path.
func TestWorkDirFromEnvWins(t *testing.T) {
	clearBridgeEnv(t)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("ALTER_BRIDGE_WORKDIR", "/srv/work")
	t.Setenv("ALTER_VAULT_DIR", "/srv/vault")

	cfg := Load()
	if cfg.WorkDir != "/srv/work" {
		t.Errorf("WorkDir = %q, want /srv/work", cfg.WorkDir)
	}
	if cfg.VaultDir != "/srv/vault" {
		t.Errorf("VaultDir = %q, want /srv/vault", cfg.VaultDir)
	}
}
