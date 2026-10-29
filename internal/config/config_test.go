package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadDefaults(t *testing.T) {
	t.Setenv("TELEGRAM_TOKEN", "")
	t.Setenv("TELEGRAM_CHAT_ID", "")
	t.Setenv("ALTER_PI_BIN", "")
	t.Setenv("ALTER_PI_PROVIDER", "")
	t.Setenv("ALTER_PI_MODEL", "")

	cfg := Load()
	if cfg.Version != "0.1.0" {
		t.Errorf("Version = %q, want 0.1.0", cfg.Version)
	}
	if cfg.TelegramToken != "" {
		t.Errorf("TelegramToken = %q, want empty", cfg.TelegramToken)
	}
	if cfg.TelegramChatID != 0 {
		t.Errorf("TelegramChatID = %d, want 0", cfg.TelegramChatID)
	}
	// Pi defaults: the binary comes from PATH and provider/model stay empty so pi
	// uses its own configuration.
	if cfg.PiBin != "pi" {
		t.Errorf("PiBin = %q, want pi", cfg.PiBin)
	}
	if cfg.PiProvider != "" {
		t.Errorf("PiProvider = %q, want empty (pi default)", cfg.PiProvider)
	}
	if cfg.PiModel != "" {
		t.Errorf("PiModel = %q, want empty (pi default)", cfg.PiModel)
	}
}

func TestLoadFromEnv(t *testing.T) {
	t.Setenv("TELEGRAM_TOKEN", "secret")
	t.Setenv("TELEGRAM_CHAT_ID", "123456789")

	cfg := Load()
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
	t.Setenv("ALTER_PI_BIN", "/home/user/.local/bin/pi")
	t.Setenv("ALTER_PI_PROVIDER", "anthropic")
	t.Setenv("ALTER_PI_MODEL", "anthropic/claude-sonnet-4:high")

	cfg := Load()
	if cfg.PiBin != "/home/user/.local/bin/pi" {
		t.Errorf("PiBin = %q, want the configured path", cfg.PiBin)
	}
	if cfg.PiProvider != "anthropic" {
		t.Errorf("PiProvider = %q, want anthropic", cfg.PiProvider)
	}
	if cfg.PiModel != "anthropic/claude-sonnet-4:high" {
		t.Errorf("PiModel = %q, want the configured pattern", cfg.PiModel)
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

// TestWorkDirDefaultsToProcessWorkingDirectory pins T18/L19: Pi's cwd defaults
// to the process working directory (the repository), not $HOME, because project
// skills and project context are discovered relative to it. $HOME is only a
// fallback for when the working directory cannot be resolved.
func TestWorkDirDefaultsToProcessWorkingDirectory(t *testing.T) {
	clearBridgeEnv(t)
	home := t.TempDir()
	t.Setenv("HOME", home)

	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("os.Getwd: %v", err)
	}

	cfg := Load()
	if cfg.WorkDir != wd {
		t.Errorf("WorkDir = %q, want the process working directory %q", cfg.WorkDir, wd)
	}
	if cfg.WorkDir == home {
		t.Errorf("WorkDir = %q: the working directory is the repository, not $HOME", cfg.WorkDir)
	}
	// The vault keeps its own default; moving the cwd must not move the vault.
	if want := filepath.Join(home, "vault"); cfg.VaultDir != want {
		t.Errorf("VaultDir = %q, want %q", cfg.VaultDir, want)
	}
}

// TestDefaultWorkDirFallbacks covers the resolver without chdir: when
// os.Getwd fails the default falls back to $HOME, and to "" (inherit) when
// neither resolves, so the bridge never invents a path.
func TestDefaultWorkDirFallbacks(t *testing.T) {
	home := t.TempDir()
	getwdErr := func() (string, error) { return "", os.ErrNotExist }
	getwdEmpty := func() (string, error) { return "", nil }
	homeErr := func() (string, error) { return "", os.ErrNotExist }

	homeOK := func() (string, error) { return home, nil }
	if got := defaultWorkDirFrom(getwdErr, homeOK); got != home {
		t.Errorf("defaultWorkDirFrom(getwd error) = %q, want $HOME %q", got, home)
	}
	if got := defaultWorkDirFrom(getwdEmpty, homeOK); got != home {
		t.Errorf("defaultWorkDirFrom(empty workdir) = %q, want $HOME %q", got, home)
	}
	if got := defaultWorkDirFrom(getwdErr, homeErr); got != "" {
		t.Errorf("defaultWorkDirFrom(no workdir, no home) = %q, want empty", got)
	}
}
