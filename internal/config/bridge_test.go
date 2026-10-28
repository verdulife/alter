package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// clearBridgeEnv empties every variable T1 introduces so a test observes pure
// defaults instead of whatever the developer has exported in the shell.
func clearBridgeEnv(t *testing.T) {
	t.Helper()
	for _, k := range []string{
		"ALTER_TELEGRAM_API_BASE",
		"ALTER_VAULT_DIR",
		"ALTER_BRIDGE_ENABLED",
		"ALTER_BRIDGE_SESSION_ID",
		"ALTER_BRIDGE_TOOLS",
		"ALTER_BRIDGE_PROMPT_FILES",
		"ALTER_BRIDGE_EXTENSIONS",
		"ALTER_BRIDGE_TIMEOUT",
	} {
		t.Setenv(k, "")
	}
}

func TestLoadBridgeDefaults(t *testing.T) {
	clearBridgeEnv(t)
	home := t.TempDir()
	t.Setenv("HOME", home)

	cfg := Load()

	if cfg.TelegramAPIBase != "https://api.telegram.org" {
		t.Errorf("TelegramAPIBase = %q, want https://api.telegram.org", cfg.TelegramAPIBase)
	}
	wantVault := filepath.Join(home, "vault")
	if cfg.VaultDir != wantVault {
		t.Errorf("VaultDir = %q, want %q", cfg.VaultDir, wantVault)
	}
	if cfg.BridgeEnabled {
		t.Error("BridgeEnabled = true, want false by default")
	}
	if cfg.BridgeSessionID != "telegram" {
		t.Errorf("BridgeSessionID = %q, want telegram", cfg.BridgeSessionID)
	}
	if cfg.BridgeTools != "read,grep,find,ls" {
		t.Errorf("BridgeTools = %q, want read,grep,find,ls", cfg.BridgeTools)
	}
	if got := len(cfg.BridgePromptFiles); got != 0 {
		t.Errorf("BridgePromptFiles = %v, want empty", cfg.BridgePromptFiles)
	}
	if got := len(cfg.BridgeExtensions); got != 0 {
		t.Errorf("BridgeExtensions = %v, want empty", cfg.BridgeExtensions)
	}
	if cfg.BridgeTimeout != 120*time.Second {
		t.Errorf("BridgeTimeout = %v, want 120s", cfg.BridgeTimeout)
	}
}

func TestLoadTelegramAPIBaseNormalizes(t *testing.T) {
	clearBridgeEnv(t)
	for _, tc := range []struct{ in, want string }{
		{"http://127.0.0.1:8081", "http://127.0.0.1:8081"},
		{"http://127.0.0.1:8081/", "http://127.0.0.1:8081"},
		{"https://api.example.test//", "https://api.example.test"},
	} {
		t.Setenv("ALTER_TELEGRAM_API_BASE", tc.in)
		if got := Load().TelegramAPIBase; got != tc.want {
			t.Errorf("TelegramAPIBase(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestLoadVaultDirFromEnv(t *testing.T) {
	clearBridgeEnv(t)
	t.Setenv("ALTER_VAULT_DIR", "/srv/vault")
	if got := Load().VaultDir; got != "/srv/vault" {
		t.Errorf("VaultDir = %q, want /srv/vault", got)
	}
}

func TestLoadBridgeFromEnv(t *testing.T) {
	clearBridgeEnv(t)
	t.Setenv("ALTER_BRIDGE_ENABLED", "true")
	t.Setenv("ALTER_BRIDGE_SESSION_ID", "owner")
	t.Setenv("ALTER_BRIDGE_TOOLS", "read,grep,save_note")
	t.Setenv("ALTER_BRIDGE_PROMPT_FILES", "/a/persona.md;/b/format.md")
	t.Setenv("ALTER_BRIDGE_EXTENSIONS", "/opt/ext/index.ts")
	t.Setenv("ALTER_BRIDGE_TIMEOUT", "45s")

	cfg := Load()
	if !cfg.BridgeEnabled {
		t.Error("BridgeEnabled = false, want true")
	}
	if cfg.BridgeSessionID != "owner" {
		t.Errorf("BridgeSessionID = %q, want owner", cfg.BridgeSessionID)
	}
	if cfg.BridgeTools != "read,grep,save_note" {
		t.Errorf("BridgeTools = %q, want read,grep,save_note", cfg.BridgeTools)
	}
	wantPrompts := []string{"/a/persona.md", "/b/format.md"}
	if len(cfg.BridgePromptFiles) != 2 || cfg.BridgePromptFiles[0] != wantPrompts[0] || cfg.BridgePromptFiles[1] != wantPrompts[1] {
		t.Errorf("BridgePromptFiles = %v, want %v", cfg.BridgePromptFiles, wantPrompts)
	}
	if len(cfg.BridgeExtensions) != 1 || cfg.BridgeExtensions[0] != "/opt/ext/index.ts" {
		t.Errorf("BridgeExtensions = %v, want [/opt/ext/index.ts]", cfg.BridgeExtensions)
	}
	if cfg.BridgeTimeout != 45*time.Second {
		t.Errorf("BridgeTimeout = %v, want 45s", cfg.BridgeTimeout)
	}
}

func TestLoadBridgeMalformedFallsBack(t *testing.T) {
	clearBridgeEnv(t)
	t.Setenv("ALTER_BRIDGE_ENABLED", "yes-please")
	t.Setenv("ALTER_BRIDGE_TIMEOUT", "soon")
	t.Setenv("ALTER_BRIDGE_PROMPT_FILES", " ; /b.md ; ")
	t.Setenv("ALTER_BRIDGE_EXTENSIONS", " ; ")

	cfg := Load()
	if cfg.BridgeEnabled {
		t.Error("BridgeEnabled = true, want false on malformed env")
	}
	if cfg.BridgeTimeout != 120*time.Second {
		t.Errorf("BridgeTimeout = %v, want 120s on malformed env", cfg.BridgeTimeout)
	}
	if len(cfg.BridgePromptFiles) != 1 || cfg.BridgePromptFiles[0] != "/b.md" {
		t.Errorf("BridgePromptFiles = %v, want [/b.md] (empty entries dropped)", cfg.BridgePromptFiles)
	}
	if len(cfg.BridgeExtensions) != 0 {
		t.Errorf("BridgeExtensions = %v, want empty (empty entries dropped)", cfg.BridgeExtensions)
	}
}

func TestLoadDotEnv(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".env")
	body := "# comment\n\nTELEGRAM_TOKEN=from-file\nexport ALTER_DB_PATH=\"/tmp/x.db\"\nQUOTED='single'\nSPACED = padded \n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write .env: %v", err)
	}

	// A real environment value must win over the file.
	t.Setenv("ALTER_DB_PATH", "/tmp/from-env")
	for _, k := range []string{"TELEGRAM_TOKEN", "QUOTED", "SPACED"} {
		t.Setenv(k, "")
	}

	if err := LoadDotEnv(path); err != nil {
		t.Fatalf("LoadDotEnv: %v", err)
	}

	if got := os.Getenv("TELEGRAM_TOKEN"); got != "from-file" {
		t.Errorf("TELEGRAM_TOKEN = %q, want from-file", got)
	}
	if got := os.Getenv("ALTER_DB_PATH"); got != "/tmp/from-env" {
		t.Errorf("ALTER_DB_PATH = %q, want /tmp/from-env (env must win)", got)
	}
	if got := os.Getenv("QUOTED"); got != "single" {
		t.Errorf("QUOTED = %q, want single", got)
	}
	if got := os.Getenv("SPACED"); got != "padded" {
		t.Errorf("SPACED = %q, want padded (spaces trimmed)", got)
	}
	if got := os.Getenv("# comment"); got != "" {
		t.Errorf("comment line leaked into env: %q", got)
	}
}

func TestLoadDotEnvMissingIsNotAnError(t *testing.T) {
	if err := LoadDotEnv(filepath.Join(t.TempDir(), "absent.env")); err != nil {
		t.Errorf("LoadDotEnv(missing) = %v, want nil", err)
	}
}
