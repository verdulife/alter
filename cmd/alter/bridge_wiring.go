package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/verdu/alter/internal/adapter/telegram"
	"github.com/verdu/alter/internal/bridge"
	"github.com/verdu/alter/internal/config"
	"github.com/verdu/alter/internal/tools"
	"github.com/verdu/alter/internal/vault"
)

// The bridge is the phase-1 Telegram ↔ Pi pipeline: the owner's chat → poller →
// persistent pi session → rich reply. Pi is the engine (S1), so this file only
// composes the existing parts; it owns no prompt logic.
const (
	// bridgeTypingAction is the transient chat state shown while Pi thinks.
	bridgeTypingAction = "typing"

	// bridgeDraftInterval caps how often one turn publishes a streaming draft.
	// Pi emits one text_delta per tiny burst, so without a cap every burst would
	// be one Bot API request. The final message always carries the whole answer,
	// so intermediate frames may be dropped.
	bridgeDraftInterval = time.Second

	// bridgeDraftID identifies the ephemeral draft of a turn: drafts sharing an
	// id replace each other, and the final message replaces them all.
	bridgeDraftID = 1

	// bridgeEmptyReply and bridgeErrorReply are the short human lines the chat
	// receives instead of a silent drop: a turn that produced no text or failed
	// must always answer something the owner can act on.
	bridgeEmptyReply = "Pi no devolvió ningún texto. Prueba a reformular el mensaje."
	bridgeErrorReply = "No pude terminar la respuesta. Inténtalo de nuevo en un momento."
)

// The two variables that configure the files Pi needs. They appear in the
// start-up error so the owner learns what to change and not only what is missing.
const (
	envBridgePromptFiles = "ALTER_BRIDGE_PROMPT_FILES"
	envBridgeExtensions  = "ALTER_BRIDGE_EXTENSIONS"
)

// bridgeAsset is one file Pi needs before the bridge can start: a markdown file
// appended to its system prompt, or an extension it must load. Both are silent
// failures when absent — Pi would answer with no persona and, worse, with no
// write tools at all — so start-up verifies them instead of trusting the paths.
type bridgeAsset struct {
	// kind is the human name of the asset in the error line.
	kind string
	// envKey is the variable that configured the path.
	envKey string
	// path is the configured path, repository-relative or absolute.
	path string
}

// verifyBridgeAssets checks that every configured prompt file and extension
// exists and is a regular file, returning one error that names the offending
// path and the variable behind it. It runs before the tools server is bound and
// before Pi can be spawned, so a misconfigured bridge never starts in a
// half-useful state.
//
// Relative paths are resolved against the bridge's own working directory (the
// repository root in a normal deployment), never against the child's WorkDir:
// the assets belong to this repository, not to $HOME, and the child starts in
// $HOME where they do not exist.
func verifyBridgeAssets(cfg config.Config) error {
	assets := make([]bridgeAsset, 0, len(cfg.BridgePromptFiles)+len(cfg.BridgeExtensions))
	for _, path := range cfg.BridgePromptFiles {
		assets = append(assets, bridgeAsset{kind: "prompt file", envKey: envBridgePromptFiles, path: path})
	}
	for _, path := range cfg.BridgeExtensions {
		assets = append(assets, bridgeAsset{kind: "extension", envKey: envBridgeExtensions, path: path})
	}

	for _, asset := range assets {
		info, err := os.Stat(asset.path)
		if err != nil {
			return fmt.Errorf("%s %q is not readable (configured by %s): %w",
				asset.kind, asset.path, asset.envKey, err)
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("%s %q is not a regular file (configured by %s)",
				asset.kind, asset.path, asset.envKey)
		}
	}
	return nil
}

// resolveBridgeAssetPaths turns the configured prompt files and extensions into
// absolute paths.
//
// verifyBridgeAssets checks them against this process's working directory (the
// repository root in a normal deployment), but Pi is spawned with WorkDir set to
// the owner's directory: with `cwd` = $HOME a relative `-e` fails to load
// ("Extension path does not exist") and a relative `--append-system-prompt` is
// silently appended as that literal string instead of the persona's content.
// Resolving here makes the same configured asset reach Pi from any directory.
func resolveBridgeAssetPaths(paths []string) []string {
	resolved := make([]string, 0, len(paths))
	for _, path := range paths {
		absolute, err := filepath.Abs(path)
		if err != nil {
			absolute = path
		}
		resolved = append(resolved, absolute)
	}
	return resolved
}

// bridgeRuntime is the composed runtime of one bridge process: the vault store,
// the loopback tools server Pi calls, the persistent Pi session and the Telegram
// poller.
type bridgeRuntime struct {
	client *telegram.Client
	poller *telegram.Poller
	tools  *tools.Server
	sess   *bridge.Session
	// store is the vault both Pi and the tools server write through. Inbound
	// attachments land in it, so it is the only place that decides a stored
	// name or a size limit.
	store *vault.Store
	// inboundMax is the inbound attachment ceiling, shared by the pre-download
	// check and the store so both enforce the same number.
	inboundMax int64

	// asks is the single pending ask_options question. Button presses reach it
	// through handleEvent and never reach Pi as text.
	asks *bridgeAskBroker

	chatID   int64
	toolsURL string
	logger   *log.Logger

	// newCmd overrides how the Pi subprocess is spawned (test seam).
	newCmd func(name string, args ...string) *exec.Cmd
	// onTeardown observes the shutdown stages in order (test seam).
	onTeardown func(stage string)
}

// bridgeRuntimeOption tunes the composed runtime. Options exist for the test
// seam, which drives the whole slice with a fake Pi.
type bridgeRuntimeOption func(*bridgeRuntime)

// withBridgeCommandFactory replaces the Pi subprocess factory, so tests run a
// fake `pi --mode rpc` instead of the real binary.
func withBridgeCommandFactory(f func(name string, args ...string) *exec.Cmd) bridgeRuntimeOption {
	return func(r *bridgeRuntime) { r.newCmd = f }
}

// withBridgeTeardownHook observes the teardown stages, so a test can prove the
// poller, the tools server and the session stop in that order.
func withBridgeTeardownHook(hook func(stage string)) bridgeRuntimeOption {
	return func(r *bridgeRuntime) { r.onTeardown = hook }
}

// withInboundFileLimit overrides the inbound attachment ceiling (test seam). It
// reaches both the vault store and the pre-download size check, because a limit
// the two disagree on would either waste a download or drop a legal file.
func withInboundFileLimit(max int64) bridgeRuntimeOption {
	return func(r *bridgeRuntime) { r.inboundMax = max }
}

// newBridgeRuntime composes the bridge: vault store → tools server → Pi session
// (carrying the tools URL and token in its environment) → poller for the owner
// chat. Nothing is spawned until the first prompt, and the tools server is bound
// before Pi can be started, so the extension always has a live endpoint.
func newBridgeRuntime(cfg config.Config, logger *log.Logger, opts ...bridgeRuntimeOption) (*bridgeRuntime, error) {
	// Start-up guard (T10): Pi's persona and, above all, its tools extension must
	// exist before anything is composed. A missing extension would leave the
	// bridge answering while Pi cannot write anything — a silent, confusing
	// failure — so refuse to start and say which path is wrong. Only bridge mode
	// checks: the legacy path has no assets to load.
	if cfg.BridgeEnabled {
		if err := verifyBridgeAssets(cfg); err != nil {
			return nil, err
		}
	}

	r := &bridgeRuntime{chatID: cfg.TelegramChatID, logger: logger, inboundMax: bridgeInboundMaxBytes}
	for _, opt := range opts {
		opt(r)
	}

	// S6: every note and attachment write goes through the vault store rooted at
	// the configured vault. The vault is only the store: Pi's cwd is WorkDir.
	store := vault.New(cfg.VaultDir, r.inboundMax)

	client := telegram.NewClientWithBase(cfg.TelegramToken, cfg.TelegramAPIBase)

	// The asker is built before the tools server, which needs it, and before the
	// poller, which feeds its button presses back in through Resolve.
	asks := newBridgeAskBroker(client, cfg.TelegramChatID, logger)

	server, err := tools.New(tools.Config{
		Vault:      store,
		Files:      bridgeFileSender{client: client, chatID: cfg.TelegramChatID},
		Asker:      asks,
		Logger:     logger,
		AskTimeout: bridgeAskTimeout,
	})
	if err != nil {
		return nil, fmt.Errorf("bridge: tools server: %w", err)
	}
	toolsURL, err := server.Start()
	if err != nil {
		_ = server.Close()
		return nil, fmt.Errorf("bridge: tools server: %w", err)
	}

	sessionOpts := []bridge.Option{bridge.WithLogger(logger)}
	if r.newCmd != nil {
		sessionOpts = append(sessionOpts, bridge.WithCommandFactory(r.newCmd))
	}
	// The extension discovers the bridge tools through these two variables.
	env := map[string]string{
		"ALTER_TOOLS_URL":   toolsURL,
		"ALTER_TOOLS_TOKEN": server.Token(),
	}
	// T12: the extension re-states the persona as the LAST block of the system
	// prompt, so it must inject the same file this process appends with
	// --append-system-prompt; otherwise a persona configured through
	// ALTER_BRIDGE_PROMPT_FILES would be contradicted by the repository copy.
	// The first configured prompt file is the persona; the rest are extra
	// contracts. The path is absolute because Pi runs in WorkDir ($HOME).
	promptFiles := resolveBridgeAssetPaths(cfg.BridgePromptFiles)
	if len(promptFiles) > 0 {
		env["ALTER_PERSONA_FILE"] = promptFiles[0]
	}

	session := bridge.NewSession(bridge.Config{
		Bin:         cfg.PiBin,
		Provider:    cfg.PiProvider,
		Model:       cfg.PiModel,
		Timeout:     cfg.BridgeTimeout,
		SessionID:   cfg.BridgeSessionID,
		Tools:       cfg.BridgeTools,
		Extensions:  resolveBridgeAssetPaths(cfg.BridgeExtensions),
		PromptFiles: promptFiles,
		// cwd = the owner's directory ($HOME by default), never the vault.
		WorkDir: cfg.WorkDir,
		Env:     env,
	}, sessionOpts...)

	r.client = client
	r.store = store
	r.asks = asks
	r.tools = server
	r.toolsURL = toolsURL
	r.sess = session
	r.poller = telegram.NewPoller(client, cfg.TelegramChatID, logger, r.handleEvent)
	return r, nil
}

// Run polls the owner chat until ctx is cancelled, then tears the runtime down
// in reverse order: poller, tools server, Pi session. The Pi process must not
// outlive the endpoint its tools call, and the poller must not outlive either.
func (r *bridgeRuntime) Run(ctx context.Context) error {
	err := r.poller.Run(ctx)
	r.teardown("poller")

	if cerr := r.tools.Close(); cerr != nil {
		r.logger.Printf("bridge: tools server shutdown: %v", cerr)
	}
	r.teardown("tools")

	r.sess.Close()
	r.teardown("session")

	if err != nil && !errors.Is(err, context.Canceled) {
		r.logger.Printf("bridge: poller stopped: %v", err)
	}
	return err
}

// teardown reports one completed shutdown stage to the test seam.
func (r *bridgeRuntime) teardown(stage string) {
	if r.onTeardown != nil {
		r.onTeardown(stage)
	}
}

// handleEvent dispatches one inbound event. Text and inbound files (S4) run the
// same turn pipeline; a button press only settles the pending ask_options
// question, so it is answered here and deliberately never handed to Pi: a
// press is not free text and must not become a prompt (S1).
func (r *bridgeRuntime) handleEvent(ctx context.Context, ev telegram.Event) error {
	chatID := ev.ChatID
	if chatID == 0 {
		chatID = r.chatID
	}
	switch ev.Kind {
	case telegram.EventText:
		return r.handleText(ctx, chatID, ev.Text)
	case telegram.EventDocument, telegram.EventPhoto:
		return r.handleMedia(ctx, chatID, ev)
	case telegram.EventCallback:
		r.asks.Resolve(ctx, ev)
		return nil
	}
	r.logger.Printf("bridge: ignoring %s event (not in this slice)", ev.Kind)
	return nil
}

// handleText runs one text turn through the shared turn pipeline.
func (r *bridgeRuntime) handleText(ctx context.Context, chatID int64, text string) error {
	return r.runTurn(ctx, chatID, text)
}

// runTurn runs one prompt: typing feedback, throttled streamed drafts, then the
// final markdown. A failed or empty turn still answers the chat with a short
// human line, never an empty message and never silence.
func (r *bridgeRuntime) runTurn(ctx context.Context, chatID int64, prompt string) error {
	if err := r.client.SendChatAction(ctx, chatID, bridgeTypingAction); err != nil {
		// Cosmetic only: the typing indicator must never fail a turn.
		r.logger.Printf("bridge: chat action: %v", err)
	}

	throttle := &draftThrottle{min: bridgeDraftInterval, now: time.Now}
	final, err := r.sess.PromptStream(ctx, prompt, func(accumulated string) {
		if !throttle.allow() {
			return
		}
		if derr := r.client.SendRichDraft(ctx, chatID, bridgeDraftID, accumulated); derr != nil {
			// A dropped draft is cosmetic too: the final message still arrives.
			r.logger.Printf("bridge: draft: %v", derr)
		}
	})

	switch {
	case err != nil:
		// The reason belongs in the log; the chat gets a line it can act on.
		r.logger.Printf("bridge: pi turn failed: %v", err)
		return r.reply(ctx, chatID, bridgeErrorReply)
	case strings.TrimSpace(final) == "":
		// A settled run without text must not become an empty Telegram message.
		return r.reply(ctx, chatID, bridgeEmptyReply)
	}
	return r.reply(ctx, chatID, final)
}

// reply delivers the final markdown with the richest representation Telegram
// accepts: a single rich message, or classic HTML when it does not fit.
func (r *bridgeRuntime) reply(ctx context.Context, chatID int64, markdown string) error {
	if err := r.client.SendFormatted(ctx, chatID, markdown); err != nil {
		return fmt.Errorf("bridge: send reply: %w", err)
	}
	return nil
}

// draftThrottle caps how often one turn publishes a draft. It is a leading-edge
// limiter: the first delta goes out immediately, so the owner sees the answer
// start, and later deltas are dropped until the interval elapses.
type draftThrottle struct {
	min  time.Duration
	now  func() time.Time
	last time.Time
}

// allow reports whether this delta may issue a Bot API request.
func (t *draftThrottle) allow() bool {
	now := t.now()
	if !t.last.IsZero() && now.Sub(t.last) < t.min {
		return false
	}
	t.last = now
	return true
}

// bridgeFileSender delivers a vault file to the owner chat: images go as photos
// so they preview inline, everything else as a document. The recipient is always
// the owner, so the model never chooses it.
type bridgeFileSender struct {
	client *telegram.Client
	chatID int64
}

// SendFile implements tools.FileSender.
func (s bridgeFileSender) SendFile(ctx context.Context, path, caption string) error {
	if isImagePath(path) {
		return s.client.SendPhoto(ctx, s.chatID, path, caption)
	}
	return s.client.SendDocument(ctx, s.chatID, path, caption)
}

// isImagePath reports whether the file can be sent as a photo. Telegram only
// accepts raster images there; everything else travels as a document.
func isImagePath(path string) bool {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".jpg", ".jpeg", ".png", ".webp":
		return true
	}
	return false
}
