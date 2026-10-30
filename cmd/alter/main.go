// Command alter runs the alter runtime: the Telegram ↔ Pi bridge plus the vault.
//
// There is exactly one entry path. The owner's chat is the only input, a
// persistent `pi --mode rpc` session is the engine, and the vault is the only
// store. There is no database and no scheduler: tasks and reminders will live in
// the vault as markdown notes in a later slice, not in a runtime this binary
// owns.
//
// The binary also answers `alter vault check [dir]`, a validator that checks the
// vault's markdown entries against the alter-vault contract and exits, and the
// reminder subcommands (`alter remind sync`, `alter remind fire`, `alter notify`)
// that join the vault with the machine's timers. They are not a second entry path:
// they take no message, start no bridge and leave the runtime untouched when no
// subcommand is given.
package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strconv"
	"syscall"

	"github.com/verdu/alter/internal/config"
)

// ignoredBridgeSwitch is the variable that used to choose between the bridge and
// the legacy database runtime. The legacy runtime is gone, so the variable no
// longer switches anything.
const ignoredBridgeSwitch = "ALTER_BRIDGE_ENABLED"

func main() {
	// The vault validator and the reminder commands are subcommands, not a second
	// runtime: they act and exit before the bridge is composed. With no subcommand
	// the process behaves exactly as before.
	if handled, code := runVaultCommand(os.Args[1:], os.Stdout, os.Stderr); handled {
		os.Exit(code)
	}
	if handled, code := runRemindCommand(os.Args[1:], os.Stdout, os.Stderr); handled {
		os.Exit(code)
	}

	logger := log.New(os.Stderr, "alter: ", log.LstdFlags)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := run(ctx, logger); err != nil {
		// One line, not a stack trace: every start-up failure carries what to fix.
		logger.Fatalf("runtime: %v", err)
	}
	logger.Printf("runtime: shutdown complete")
}

// run is the whole process lifetime: load and validate the configuration, compose
// the single runtime and serve until ctx is cancelled. A cancelled context is the
// normal shutdown, not a failure.
func run(ctx context.Context, logger *log.Logger) error {
	cfg, err := loadConfig(logger)
	if err != nil {
		return err
	}

	warnIgnoredBridgeSwitch(logger)

	rt, err := newBridgeRuntime(cfg, logger)
	if err != nil {
		return err
	}
	logger.Printf("runtime: alter %s started (vault=%s, workdir=%s, session=%q)",
		cfg.Version, cfg.VaultDir, cfg.WorkDir, cfg.BridgeSessionID)

	if err := rt.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
		return err
	}
	return nil
}

// loadConfig reads the environment (after .env) and refuses to start when a piece
// the bridge cannot work without is missing. Each failure names the variable, so
// the owner can fix it without reading the source.
func loadConfig(logger *log.Logger) (config.Config, error) {
	// Load .env before reading configuration, so the runtime starts without a
	// shell wrapper (`set -a; source .env; set +a`). Exported variables still win
	// over the file, and a missing file is not an error.
	if err := config.LoadDotEnv(".env"); err != nil {
		logger.Printf("runtime: .env: %v", err)
	}

	cfg := config.Load()
	if cfg.TelegramToken == "" {
		return config.Config{}, fmt.Errorf("TELEGRAM_TOKEN is required: the bridge has no channel without the bot token")
	}
	if cfg.TelegramChatID == 0 {
		return config.Config{}, fmt.Errorf("TELEGRAM_CHAT_ID is required: the bridge serves exactly one owner chat")
	}
	return cfg, nil
}

// warnIgnoredBridgeSwitch says one line when the retired ALTER_BRIDGE_ENABLED
// survives in the environment with a value that would have disabled the bridge.
// Ignoring it silently would let an operator believe the variable still decides
// something; a truthful "on" is not worth a line because it matches what the
// process does anyway.
func warnIgnoredBridgeSwitch(logger *log.Logger) {
	raw := os.Getenv(ignoredBridgeSwitch)
	if raw == "" {
		return
	}
	if enabled, err := strconv.ParseBool(raw); err == nil && enabled {
		return
	}
	logger.Printf("runtime: %s=%q is ignored: the Telegram <-> Pi bridge is the only runtime and cannot be disabled", ignoredBridgeSwitch, raw)
}
