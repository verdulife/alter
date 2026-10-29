package main

import (
	"context"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// TestRuntimeCreatesNoDatabase pins the single-runtime guarantee: alter is the
// Telegram ↔ Pi bridge plus the vault, so starting it must touch no database at
// all. ALTER_DB_PATH points inside a directory that must never appear, because
// SQLite creates both the file and its parent on demand — a stale wiring would
// leave that directory behind even with a run that produces nothing else.
//
// A cancelled context starts and stops the process without polling Telegram or
// spawning Pi: the poller checks the context before its first request, so the
// test observes the composition and the shutdown and nothing external.
func TestRuntimeCreatesNoDatabase(t *testing.T) {
	dir := t.TempDir()
	persona := writeSmokeAsset(t, dir, "persona.md", "# persona\n")
	extension := writeSmokeAsset(t, dir, "index.ts", "// extension\n")

	dbDir := filepath.Join(dir, "db")
	dbPath := filepath.Join(dbDir, "alter.db")
	t.Setenv("ALTER_DB_PATH", dbPath)

	t.Setenv("TELEGRAM_TOKEN", testBotToken)
	t.Setenv("TELEGRAM_CHAT_ID", strconv.FormatInt(testOwnerID, 10))
	t.Setenv("ALTER_TELEGRAM_API_BASE", "http://127.0.0.1:9")
	t.Setenv("ALTER_VAULT_DIR", filepath.Join(dir, "vault"))
	t.Setenv("ALTER_BRIDGE_WORKDIR", dir)
	t.Setenv("ALTER_BRIDGE_PROMPT_FILES", persona)
	t.Setenv("ALTER_BRIDGE_EXTENSIONS", extension)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if err := run(ctx, discardLogger()); err != nil {
		t.Fatalf("run = %v, want a clean shutdown", err)
	}

	if _, err := os.Stat(dbPath); !os.IsNotExist(err) {
		t.Errorf("stat %s = %v, want the database file never created", dbPath, err)
	}
	if _, err := os.Stat(dbDir); !os.IsNotExist(err) {
		t.Errorf("stat %s = %v, want not even its directory created", dbDir, err)
	}
}

// TestStartupFailsWithOneClearMessage pins the other half of the single entry
// path: without the two pieces the bridge cannot work with, the process stops
// with one error that names the variable to set instead of starting a runtime
// that silently answers nothing.
func TestStartupFailsWithOneClearMessage(t *testing.T) {
	t.Setenv("TELEGRAM_TOKEN", "123456:TEST-TOKEN")
	t.Setenv("TELEGRAM_CHAT_ID", "4242")

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	for _, tc := range []struct{ name, env, value, wantVar string }{
		{name: "missing token", env: "TELEGRAM_TOKEN", value: "", wantVar: "TELEGRAM_TOKEN"},
		{name: "missing chat id", env: "TELEGRAM_CHAT_ID", value: "", wantVar: "TELEGRAM_CHAT_ID"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(tc.env, tc.value)

			err := run(ctx, discardLogger())
			if err == nil {
				t.Fatal("run = nil, want a start-up error")
			}
			if !strings.Contains(err.Error(), tc.wantVar) {
				t.Errorf("error %q does not name %q, so the owner cannot fix it", err, tc.wantVar)
			}
		})
	}
}

// TestIgnoredBridgeSwitchIsLoggedOnce pins the removal of ALTER_BRIDGE_ENABLED as
// a switch. The bridge is the only runtime, so a value that says "off" no longer
// turns anything off; leaving it silent would let an operator believe the
// variable still decides something. A truthful "on" needs no line.
func TestIgnoredBridgeSwitchIsLoggedOnce(t *testing.T) {
	var out strings.Builder
	logger := log.New(&out, "", 0)

	t.Setenv("ALTER_BRIDGE_ENABLED", "false")
	warnIgnoredBridgeSwitch(logger)
	if !strings.Contains(out.String(), "ALTER_BRIDGE_ENABLED") {
		t.Errorf("log %q does not mention ALTER_BRIDGE_ENABLED, so the operator never learns it is ignored", out.String())
	}
	if lines := strings.Count(strings.TrimSpace(out.String()), "\n") + 1; lines > 2 {
		t.Errorf("logged %d lines, want at most one warning (%q)", lines, out.String())
	}

	out.Reset()
	t.Setenv("ALTER_BRIDGE_ENABLED", "true")
	warnIgnoredBridgeSwitch(logger)
	if out.String() != "" {
		t.Errorf("log %q, want nothing when the variable matches reality", out.String())
	}

	out.Reset()
	t.Setenv("ALTER_BRIDGE_ENABLED", "")
	warnIgnoredBridgeSwitch(logger)
	if out.String() != "" {
		t.Errorf("log %q, want nothing when the variable is unset", out.String())
	}
}
