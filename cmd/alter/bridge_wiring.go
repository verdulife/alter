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
	"sync"
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

	// bridgeTypingInterval caps how often one turn refreshes the typing indicator.
	// Telegram honours one chat action for about five seconds, so a turn that lasts
	// minutes — the normal case — has to repeat it well inside that window; without
	// the refresh the owner saw the indicator vanish while Pi was still working.
	bridgeTypingInterval = 4 * time.Second

	// bridgeProgressAfter is how long a turn may run before the chat is told, once,
	// that it is still working. Telegram's typing indicator lasts seconds and the
	// streamed drafts may be silent for minutes, so without this line a long turn
	// looked exactly like a dead one to the owner.
	bridgeProgressAfter = 45 * time.Second

	// bridgeProgressLine is that notice. It is deliberately short and says only
	// that the turn is alive: the answer still arrives as its own, later message.
	bridgeProgressLine = "Sigo con esto, dame un momento."

	// bridgeDraftInterval caps how often one turn publishes a streaming draft.
	// Pi emits one text_delta per tiny burst, so without a cap every burst would
	// be one Bot API request. The final message always carries the whole answer,
	// so intermediate frames may be dropped.
	bridgeDraftInterval = time.Second

	// bridgeThinkingInterval is how often a running turn re-publishes the thinking
	// draft while it has produced no text. Telegram Web renders neither the typing
	// action nor anything else for the minutes Pi may spend before its first delta
	// — a turn parked on an approval or a question can show nothing at all — so the
	// draft is refreshed well inside the window the owner would otherwise read as a
	// dead turn. Four seconds matches bridgeTypingInterval and keeps the cost at one
	// cheap request per interval instead of a burst.
	bridgeThinkingInterval = 4 * time.Second

	// bridgeAnnounceTimeout bounds the thinking draft the bridge sends when a
	// message arrives. That send happens in the poll loop, before the turn is
	// queued, so a slow or unreachable Bot API can delay the next poll by at most
	// this much instead of the client's own forty-second timeout.
	bridgeAnnounceTimeout = 3 * time.Second

	// bridgeDraftID identifies the ephemeral draft of a turn: drafts sharing an
	// id replace each other, and the final message replaces them all.
	bridgeDraftID = 1

	// bridgeThinkingDraft is the placeholder of every turn. It is the only
	// liveness signal a Telegram Web client renders for a bot, and the owner asked
	// for the bare ellipsis rather than a word: it reads as an animation, not as
	// text competing with the answer.
	bridgeThinkingDraft = "…"

	// bridgeEmptyReply and bridgeErrorReply are the short human lines the chat
	// receives instead of a silent drop: a turn that produced no text or failed
	// must always answer something the owner can act on.
	bridgeEmptyReply = "Pi no devolvió ningún texto. Prueba a reformular el mensaje."
	bridgeErrorReply = "No pude terminar la respuesta. Inténtalo de nuevo en un momento."

	// bridgeDialogTitleLimit and bridgeDialogMessageLimit bound how much of a Pi
	// dialog reaches the chat. An approval must show the command it is about, and a
	// multi-line command is only judgeable with its line breaks, so the message
	// budget leaves room for a typical script; the clip below keeps both ends for
	// the extreme case instead of letting one command fill the conversation.
	bridgeDialogTitleLimit   = 120
	bridgeDialogMessageLimit = 800

	// bridgeDialogEllipsis marks the elided middle of a clipped dialog text.
	bridgeDialogEllipsis = "…"

	// bridgeDialogSeparator is the unit separator the Pi extension puts between
	// the reason and the command of an approval message. The extension sends data,
	// not markup, so the bridge renders both parts here: inside a markdown fence
	// this renderer keeps HTML entities literal, which turned a `>` in a command
	// into `&gt;` on the phone and let three backticks close the fence early. No
	// shell command carries the character, so it never appears inside a command.
	bridgeDialogSeparator = "\x1f"

	// bridgeDialogHeadShare is the fraction of a clipped text given to the head;
	// the rest goes to the tail. It is the same split the Pi extension applies to
	// the argument preview, so the two caps agree instead of fighting.
	bridgeDialogHeadShare = 2
	bridgeDialogShareOf   = 3

	// bridgeResetCommand is the one text the bridge consumes itself: it starts a
	// fresh Pi conversation without restarting the service, the poller or the tools
	// server. The match is exact, so "/reset algo" stays an ordinary prompt.
	bridgeResetCommand = "/reset"

	// bridgeResetBaseID is the session id the reset builds its timestamps on when
	// no base id is configured, so the id always says which channel it belongs to.
	bridgeResetBaseID = "telegram"

	// bridgeResetIDLayout is the timestamp layout of a reset session id: readable
	// in the chat and in Pi's own session list.
	bridgeResetIDLayout = "20060102-150405"
)

// The three answers a /reset can produce. A reset is a command of its own, so it
// always answers something: the new id, why it was refused, or why it failed.
const (
	bridgeResetReply       = "Conversación reiniciada. Nuevo id de sesión: %s"
	bridgeResetBusyReply   = "Hay un turno en marcha: espera a que termine y vuelve a enviar /reset."
	bridgeResetFailedReply = "No pude reiniciar la conversación. Inténtalo de nuevo en un momento."
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
// `send_file` and no `ask_options`, so the chat could neither receive a vault
// file nor answer a question with buttons — so start-up verifies them instead of
// trusting the paths.
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
	// store is the vault inbound attachments land in, so it is the only place
	// that decides a stored name or a size limit. Pi writes it with its own
	// `write`/`edit`, and the tools server only reads the root to keep `send_file`
	// inside it.
	store *vault.Store
	// inboundMax is the inbound attachment ceiling, shared by the pre-download
	// check and the store so both enforce the same number.
	inboundMax int64

	// asks is the single pending question: an ask_options question or a tool
	// approval. Button presses reach it through handleEvent and never reach Pi as
	// text.
	asks *bridgeAskBroker

	// turnMu keeps one turn's typing, drafts and replies from interleaving with
	// another's: the Pi session already serializes prompts, but the chat would
	// still see two halves of two answers.
	turnMu sync.Mutex
	// turns tracks the turn goroutines started by the poller, so teardown can wait
	// for them before it takes the session and the tools server away.
	turns sync.WaitGroup
	// draft is the shared state of the one streaming draft: every turn publishes
	// into the same draft id, so the bridge must know whether the visible draft
	// already carries a turn's answer before it publishes the placeholder over it.
	// Both sides claim it before they send, so the answer wins the decision even
	// against a placeholder that is already on the wire.
	draft draftOwnership

	// turnGate, when set, runs inside the turn goroutine right after it owns the
	// turn slot (test seam): it lets a test hold a turn open, so it can prove that
	// Run waits for it and that turnMu serializes the turns behind it.
	turnGate func(ctx context.Context)

	chatID   int64
	toolsURL string
	logger   *log.Logger

	// newCmd overrides how the Pi subprocess is spawned (test seam).
	newCmd func(name string, args ...string) *exec.Cmd
	// onTeardown observes the shutdown stages in order (test seam).
	onTeardown func(stage string)

	// sessionBase is the configured session id, kept so a reset can show it in the
	// log and derive the new id from it. The live id belongs to the session, which
	// owns the spawn; this is only the configured base.
	sessionBase string
	// typingInterval is how often a running turn refreshes the typing indicator.
	// The field is the test seam for the refresher; production always uses
	// bridgeTypingInterval.
	typingInterval time.Duration
	// progressAfter is how long a turn runs before the chat gets the progress
	// line. The field is the test seam for the notice; production always uses
	// bridgeProgressAfter.
	progressAfter time.Duration
	// thinkingInterval is how often a waiting turn re-publishes the thinking
	// draft. The field is the test seam for the refresher; production always uses
	// bridgeThinkingInterval.
	thinkingInterval time.Duration
	// resetSession performs the reset a /reset asks for (test seam): the real one
	// is Session.Reset, and a test can make it fail to pin the error line.
	resetSession func(ctx context.Context, sessionID string) error
	// syncReminders runs the reminder reconciliation once at start-up (test seam):
	// the real one is `alter remind sync`, and a test can replace it to keep the
	// machine's systemd out of the run.
	syncReminders func(ctx context.Context)
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

// withTurnGate holds a turn open from inside the turn goroutine (test seam), so a
// test can observe teardown waiting for it and the slot serializing the next one.
func withTurnGate(gate func(ctx context.Context)) bridgeRuntimeOption {
	return func(r *bridgeRuntime) { r.turnGate = gate }
}

// withInboundFileLimit overrides the inbound attachment ceiling (test seam). It
// reaches both the vault store and the pre-download size check, because a limit
// the two disagree on would either waste a download or drop a legal file.
func withInboundFileLimit(max int64) bridgeRuntimeOption {
	return func(r *bridgeRuntime) { r.inboundMax = max }
}

// withSessionReset replaces the reset a /reset command performs (test seam), so a
// test can pin what the chat receives when the reset fails.
func withSessionReset(f func(ctx context.Context, sessionID string) error) bridgeRuntimeOption {
	return func(r *bridgeRuntime) { r.resetSession = f }
}

// withTypingInterval replaces how often a turn refreshes the typing indicator (test
// seam), so a test can watch the refresh inside a turn that would otherwise last
// four seconds per tick.
func withTypingInterval(interval time.Duration) bridgeRuntimeOption {
	return func(r *bridgeRuntime) { r.typingInterval = interval }
}

// withProgressAfter replaces how long a turn runs before the chat gets the
// progress line (test seam), so a test can observe the notice inside a turn that
// would otherwise last forty-five seconds.
func withProgressAfter(after time.Duration) bridgeRuntimeOption {
	return func(r *bridgeRuntime) { r.progressAfter = after }
}

// withThinkingInterval replaces how often a waiting turn re-publishes the thinking
// draft (test seam), so a test can watch the refresh inside a turn that would
// otherwise last four seconds per tick.
func withThinkingInterval(interval time.Duration) bridgeRuntimeOption {
	return func(r *bridgeRuntime) { r.thinkingInterval = interval }
}

// newBridgeRuntime composes the bridge: vault store → tools server → Pi session
// (carrying the tools URL and token in its environment) → poller for the owner
// chat. Nothing is spawned until the first prompt, and the tools server is bound
// before Pi can be started, so the extension always has a live endpoint.
func newBridgeRuntime(cfg config.Config, logger *log.Logger, opts ...bridgeRuntimeOption) (*bridgeRuntime, error) {
	// Start-up guard (T10): Pi's persona and, above all, its tools extension must
	// exist before anything is composed. A missing extension would leave the
	// bridge answering while Pi can neither send a vault file nor ask a question
	// with buttons — a silent, confusing failure — so refuse to start and say
	// which path is wrong. The bridge is the only runtime, so the guard always
	// runs.
	if err := verifyBridgeAssets(cfg); err != nil {
		return nil, err
	}

	r := &bridgeRuntime{
		chatID:           cfg.TelegramChatID,
		logger:           logger,
		inboundMax:       bridgeInboundMaxBytes,
		typingInterval:   bridgeTypingInterval,
		progressAfter:    bridgeProgressAfter,
		thinkingInterval: bridgeThinkingInterval,
	}
	for _, opt := range opts {
		opt(r)
	}

	// S6: inbound attachments are written through the vault store rooted at the
	// configured vault, and the tools server is given that same root so
	// `send_file` can refuse any path that leaves it. Pi writes the vault with its
	// own tools; the vault is only the store, Pi's cwd is WorkDir.
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
	// contracts. The path is absolute because Pi runs in WorkDir, which is not
	// necessarily the bridge's own directory.
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
		// T13: every blocking dialog Pi raises is answered here, by the owner's
		// button press or with a cancellation.
		OnDialog: r.onDialog,
	}, sessionOpts...)

	r.client = client
	r.store = store
	r.asks = asks
	r.tools = server
	r.toolsURL = toolsURL
	r.sess = session
	// /reset keeps the configured base id visible in the log and derives the new
	// id from it; the real reset is the session's, unless a test replaced it.
	r.sessionBase = cfg.BridgeSessionID
	if r.resetSession == nil {
		r.resetSession = session.Reset
	}
	r.poller = telegram.NewPoller(client, cfg.TelegramChatID, logger, r.handleEvent)
	// F3-3: the timers are derived from the vault, so a restart leaves nothing
	// scheduled and a reminder may have come due while the machine was off.
	// Reconciling is composed here and fired by Run, which keeps the composition
	// itself free of side effects.
	r.syncReminders = func(ctx context.Context) { startupReminderSync(ctx, cfg, logger) }
	return r, nil
}

// Run polls the owner chat until ctx is cancelled, then tears the runtime down
// in reverse order: poller, tools server, Pi session. The Pi process must not
// outlive the endpoint its tools call, and the poller must not outlive either.
func (r *bridgeRuntime) Run(ctx context.Context) error {
	// The reminder reconciler runs beside the poller and never in front of it: it may
	// have to wake the agent for a reminder that came due while the machine was off,
	// and a failure is logged instead of stopping the bridge.
	go r.syncReminders(ctx)

	err := r.poller.Run(ctx)
	// A turn started by the poll loop may still be running: it must finish (or be
	// cancelled with the same context) before the session and the tools server it
	// uses disappear.
	r.turns.Wait()
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
// same turn pipeline; a button press only settles the pending question (an
// ask_options question or a tool approval), so it is answered here and
// deliberately never handed to Pi: a press is not free text and must not become
// a prompt (S1). A typed message may settle that same question instead of
// starting a turn, which is why handleText always asks the broker first.
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
		// The press is the answer that unblocks a parked turn, so it is itself the
		// trigger for feedback: the turn published its placeholder minutes ago and
		// Pi still needs a moment before the next text. Publishing here — before the
		// broker hands the answer to Pi — is what puts the placeholder between the
		// press and that text. A press on a button that settles nothing is skipped,
		// so a stale press never replaces a live answer.
		if r.asksWaiting() {
			r.announceThinking(ctx, chatID)
		}
		r.asks.Resolve(ctx, ev)
		return nil
	}
	r.logger.Printf("bridge: ignoring %s event (not in this slice)", ev.Kind)
	return nil
}

// onDialog answers one blocking UI dialog Pi raised. confirm becomes the approval
// prompt with two buttons, select becomes one button per option, and input/editor
// are dismissed with cancelled: a free-text answer would need a reply the bridge
// cannot collect, and guessing one would defeat the gate (T13).
func (r *bridgeRuntime) onDialog(ctx context.Context, req bridge.DialogRequest) (bridge.DialogResponse, error) {
	switch req.Method {
	case "confirm":
		approved, err := r.asks.Approve(ctx, r.dialogQuestion(req))
		if err != nil {
			return bridge.DialogResponse{}, err
		}
		return bridge.DialogResponse{Confirmed: approved}, nil

	case "select":
		if len(req.Options) < 2 || len(req.Options) > 4 {
			// The broker posts between 2 and 4 buttons: a one-choice question has
			// nothing to press and a longer list would silently drop options, so
			// the honest answer is a cancellation.
			return bridge.DialogResponse{}, fmt.Errorf("select offers %d options; the bridge can ask between 2 and 4", len(req.Options))
		}
		choice, err := r.asks.AskOptions(ctx, r.dialogQuestion(req), req.Options)
		if err != nil {
			return bridge.DialogResponse{}, err
		}
		return bridge.DialogResponse{Value: choice}, nil

	default: // input, editor
		r.logger.Printf("bridge: %s dialog %q needs a free text answer, which the bridge cannot ask for; cancelling it",
			req.Method, req.ID)
		return bridge.DialogResponse{Cancelled: true}, nil
	}
}

// dialogQuestion renders a dialog's title and message as the text of the button
// prompt. The text is escaped and clipped because it carries model-controlled
// arguments (the command about to run): markup from a tool call must never become
// buttons or formatting in the owner's chat.
func (r *bridgeRuntime) dialogQuestion(req bridge.DialogRequest) string {
	title := strings.TrimSpace(req.Title)
	if title == "" {
		// The same short human question the Pi extension asks: a dialog with no title
		// of its own must not reach the owner as the bare word "Aprobación".
		title = "¿Lo permito?"
	}

	var out strings.Builder
	out.WriteString(escapeAskText(clipDialogText(title, bridgeDialogTitleLimit)))
	if message := strings.TrimSpace(req.Message); message != "" {
		out.WriteString("\n\n")
		out.WriteString(dialogMessage(message))
	}
	return out.String()
}

// dialogMessage renders the message of a dialog as markup. The Pi extension sends
// the reason and the command separated by bridgeDialogSeparator, and the bridge
// owns the rendering: both parts are escaped as text before they enter the markup,
// and the command goes inside a <pre> block, where `>`, `<`, `&` and backticks
// reach the owner literally and a `<tg-button>` in a command stays text.
//
// A message without the separator (any other UI source) has no command to show,
// so it keeps the behaviour it always had: escaped and clipped as plain text.
func dialogMessage(message string) string {
	reason, command, ok := strings.Cut(message, bridgeDialogSeparator)
	if !ok {
		return escapeAskText(clipDialogText(message, bridgeDialogMessageLimit))
	}

	// The clip runs on the command text, never on the markup: the head and the tail
	// are what the owner decides on, and cutting the escaped block could break a
	// tag in half.
	block := escapeAskText(clipDialogText(strings.TrimSpace(command), bridgeDialogMessageLimit))
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return "<pre>" + block + "</pre>"
	}
	return escapeAskText(clipDialogText(reason, bridgeDialogMessageLimit)) + "\n\n<pre>" + block + "</pre>"
}

// clipDialogText caps text at limit runes while keeping BOTH its head and its
// tail: the dangerous part of a long command usually sits at the end
// (`... && rm -rf ~/x`), so a head-only cut would hide exactly what the approval
// exists to show. Two thirds go to the head and one third to the tail; the Pi
// extension composes its message inside this cap with the same split, so a
// message it already fitted is never re-cut here.
// The elided middle is marked with an ellipsis, so the owner knows it is partial.
func clipDialogText(text string, limit int) string {
	runes := []rune(text)
	if limit <= 0 || len(runes) <= limit {
		return text
	}
	head := limit * bridgeDialogHeadShare / bridgeDialogShareOf
	tail := limit - head - len([]rune(bridgeDialogEllipsis))
	if tail <= 0 {
		// A limit too small to hold both ends is not a case this prompt uses; fall
		// back to the head-only cut rather than dropping the text entirely.
		return truncateAskText(text, limit)
	}
	return string(runes[:head]) + bridgeDialogEllipsis + string(runes[len(runes)-tail:])
}

// handleText routes one text message: /reset is the only text the bridge consumes
// as a command, so it can never reach Pi as a prompt. Every other message first
// gets the chance to answer the pending question — a question with options takes
// the text as its answer, an approval only takes an unambiguous word — and only
// a message that settles nothing (or that cancelled an approval) runs a turn.
func (r *bridgeRuntime) handleText(ctx context.Context, chatID int64, text string) error {
	if isBridgeReset(text) {
		return r.resetConversation(ctx, chatID)
	}
	if r.asks != nil {
		// A typed answer resumes the turn the same way a press does: the parked turn
		// emitted its placeholder long ago and Pi needs a moment to continue, so the
		// message republishes it before the broker wakes Pi up.
		if r.asksWaiting() {
			r.announceThinking(ctx, chatID)
		}
		if outcome := r.asks.ResolveText(text); outcome.settled() {
			// The tool call that just returned is invisible to the owner, so the
			// chat always learns what his message did to the question.
			if err := r.reply(ctx, chatID, askTextConfirmation(outcome)); err != nil {
				return err
			}
			if !outcome.passesTextOn() {
				return nil
			}
		}
	}
	return r.runTurn(ctx, chatID, text)
}

// isBridgeReset reports whether the message is the reset command. The match is
// exact after trimming the surrounding whitespace Telegram may add, so a message
// that says something else ("/reset algo") is never turned into a reset.
func isBridgeReset(text string) bool {
	return strings.TrimSpace(text) == bridgeResetCommand
}

// resetConversation starts a fresh Pi conversation from the chat and answers with
// the new session id. It runs in the poll loop instead of the turn pipeline on
// purpose: the reset never interrupts a turn (Session.Reset refuses while one is
// in flight), so the honest answer while Pi is busy is an immediate line the owner
// can act on, not a command queued behind a turn that may run for the whole
// ALTER_BRIDGE_TIMEOUT.
func (r *bridgeRuntime) resetConversation(ctx context.Context, chatID int64) error {
	id := bridgeResetSessionID(r.sessionBase, time.Now())
	if err := r.resetSession(ctx, id); err != nil {
		if errors.Is(err, bridge.ErrTurnInFlight) {
			r.logger.Printf("bridge: /reset refused: a turn is in flight, so the session was not reset (base id %q)", r.sessionBase)
			return r.reply(ctx, chatID, bridgeResetBusyReply)
		}
		r.logger.Printf("bridge: /reset failed (base id %q, new session id %q): %v", r.sessionBase, id, err)
		return r.reply(ctx, chatID, bridgeResetFailedReply)
	}
	r.logger.Printf("bridge: /reset: conversation reset (base id %q, new session id %q)", r.sessionBase, id)
	return r.reply(ctx, chatID, fmt.Sprintf(bridgeResetReply, id))
}

// bridgeResetSessionID builds the id of the conversation a reset starts: the
// configured base id plus a UTC timestamp, so the owner sees in the confirmation
// (and in Pi's own session list) which conversation is current. A reset always
// replaces the child, so second granularity is enough: two resets inside the same
// second only reuse an id whose transcript is empty.
func bridgeResetSessionID(base string, now time.Time) string {
	if base == "" {
		base = bridgeResetBaseID
	}
	return base + "-" + now.UTC().Format(bridgeResetIDLayout)
}

// asksWaiting reports whether a question is parked waiting for the owner. It is
// what keeps the resume announcement honest: only the answer that really settles a
// dialogue republishes the thinking draft, so a stale button in an old message
// never replaces the answer text of the turn that is streaming right now.
//
// The pending slot belongs to the broker (bridge_ask.go), which owns every
// invariant around it; this only reads it under the same lock the broker takes.
func (r *bridgeRuntime) asksWaiting() bool {
	if r.asks == nil {
		return false
	}
	r.asks.mu.Lock()
	defer r.asks.mu.Unlock()
	return r.asks.pending != nil
}

// runTurn starts one prompt: typing feedback, throttled streamed drafts, then the
// final markdown. A failed or empty turn still answers the chat with a short
// human line, never an empty message and never silence.
//
// The turn runs on its own goroutine because the poll loop must keep receiving
// while Pi works: an approval button, an ask_options press and the next message
// all arrive there, and a poll loop blocked inside a turn would leave the owner
// pressing buttons nobody reads. turnMu keeps the turns themselves serialized.
func (r *bridgeRuntime) runTurn(ctx context.Context, chatID int64, prompt string) error {
	// Announce the turn when its message arrives, not when it starts running: a
	// turn queued behind another one would otherwise show nothing at all while it
	// waits for the slot, which is exactly what made a sent link look dropped.
	// The send is bounded and never fails the turn; the turn's own refresher keeps
	// the announcement alive once it owns the slot.
	r.announceThinking(ctx, chatID)

	r.turns.Add(1)
	go func() {
		defer r.turns.Done()
		r.turnMu.Lock()
		defer r.turnMu.Unlock()
		if r.turnGate != nil {
			r.turnGate(ctx)
		}
		if err := r.promptTurn(ctx, chatID, prompt); err != nil {
			// The poller no longer waits for this turn, so its failure is logged
			// here instead of travelling back to the poll loop.
			r.logger.Printf("bridge: turn: %v", err)
		}
	}()
	return nil
}

// promptTurn is the body of one turn.
func (r *bridgeRuntime) promptTurn(ctx context.Context, chatID int64, prompt string) error {
	// The indicator is refreshed for the whole turn, approvals and questions
	// included: those waits happen inside the turn, and a single chat action would
	// leave the owner looking at nothing while Pi works.
	stopTyping := r.keepTypingAlive(ctx, chatID)
	defer stopTyping()

	// Telegram Web does not render the typing indicator for bots, but it does
	// render drafts, which is the channel that already shows the answer as it is
	// written. This refresher publishes the placeholder at once — the arrival
	// announcement already did, and repeating it is one cheap request into the same
	// draft id — and again every interval while Pi has produced no text, so a turn
	// parked on an approval or a question stays visibly alive.
	stopThinking := r.keepThinkingAlive(ctx, chatID)
	defer stopThinking()

	// A turn that runs past the threshold announces itself once: silence for
	// minutes is indistinguishable from a dead turn in the chat.
	stopProgress := r.notifyProgress(ctx, chatID)
	defer stopProgress()

	throttle := &draftThrottle{min: bridgeDraftInterval, now: time.Now}
	final, err := r.sess.PromptStream(ctx, prompt, func(accumulated string) {
		if !throttle.allow() {
			return
		}
		// Claim the draft for the answer before sending it, so that deciding
		// whether a placeholder may go out and taking the draft for the answer are
		// one step: from here on the refresher stops and any placeholder already in
		// flight repairs itself with this text.
		r.draft.claimText(accumulated)
		if derr := r.client.SendRichDraft(ctx, chatID, bridgeDraftID, accumulated); derr != nil {
			// A dropped draft is cosmetic too: the final message still arrives.
			r.logger.Printf("bridge: draft: %v", derr)
		}
	})

	// Stop the refresher and the notice — waiting for sends already in flight —
	// before the answer goes out. A placeholder published after the final message
	// would appear as a stray "Pensando…" underneath it.
	stopThinking()
	stopProgress()
	r.draft.releaseText()

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

// bridgeChatAction is the Bot API surface the typing refresher needs: one transient
// chat state.
type bridgeChatAction interface {
	SendChatAction(ctx context.Context, chatID int64, action string) error
}

// bridgeTextSender is the Bot API surface the progress notice needs: one plain
// chat message. Plain text is what keeps the notice apart from the final reply,
// which always goes out through the rich-message path.
type bridgeTextSender interface {
	SendMessage(ctx context.Context, chatID int64, text string) error
}

// progressNotifier tells the chat once that a long turn is still alive. It exists
// because the typing indicator and the streamed drafts both go quiet: a turn
// waiting on Pi, or on an approval, can show nothing for minutes.
type progressNotifier struct {
	sender bridgeTextSender
	chatID int64
	text   string
	after  time.Duration
	logger *log.Logger
}

// start sends the line once, after the threshold, and nothing else: a fast turn
// stops it before the timer fires, so the chat only hears about turns that really
// are long. The returned stop is safe to call more than once and waits for the
// goroutine, so a finished turn leaves nothing behind; a cancelled ctx ends the
// goroutine by itself.
func (p progressNotifier) start(ctx context.Context) (stop func()) {
	after := p.after
	if after <= 0 {
		after = bridgeProgressAfter
	}

	stopCh := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		timer := time.NewTimer(after)
		defer timer.Stop()
		select {
		case <-stopCh:
			return
		case <-ctx.Done():
			return
		case <-timer.C:
			p.send(ctx)
		}
	}()

	var once sync.Once
	return func() {
		once.Do(func() { close(stopCh) })
		<-done
	}
}

// send emits the notice. A failure is logged only: the turn must never fail
// because a courtesy line did not arrive.
func (p progressNotifier) send(ctx context.Context) {
	if err := p.sender.SendMessage(ctx, p.chatID, p.text); err != nil && p.logger != nil {
		p.logger.Printf("bridge: progress notice: %v", err)
	}
}

// notifyProgress starts the progress notice of one turn and returns the function
// that stops it.
func (r *bridgeRuntime) notifyProgress(ctx context.Context, chatID int64) (stop func()) {
	after := r.progressAfter
	if after <= 0 {
		after = bridgeProgressAfter
	}
	return progressNotifier{
		sender: r.client,
		chatID: chatID,
		text:   bridgeProgressLine,
		after:  after,
		logger: r.logger,
	}.start(ctx)
}

// typingRefresher keeps one chat action alive for as long as a turn runs. Telegram
// honours a chat action for about five seconds, so a turn that lasts minutes needs
// a refresh well inside that window or the indicator vanishes while Pi is still
// working.
type typingRefresher struct {
	sender bridgeChatAction
	chatID int64
	action string
	every  time.Duration
	logger *log.Logger
}

// start emits the action at once — so the indicator appears before Pi is prompted —
// and repeats it every interval until stop is called or ctx ends. A failed request
// is logged and never stops the refresh, and never fails or waits on the turn: the
// indicator is cosmetic. The returned stop is safe to call more than once and waits
// for the goroutine, so a finished turn leaves nothing behind; a cancelled ctx ends
// the goroutine by itself.
func (t typingRefresher) start(ctx context.Context) (stop func()) {
	t.send(ctx)

	every := t.every
	if every <= 0 {
		every = bridgeTypingInterval
	}

	stopCh := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(every)
		defer ticker.Stop()
		for {
			select {
			case <-stopCh:
				return
			case <-ctx.Done():
				return
			case <-ticker.C:
				t.send(ctx)
			}
		}
	}()

	var once sync.Once
	return func() {
		once.Do(func() { close(stopCh) })
		<-done
	}
}

// send emits one chat action. A failure is logged only: the turn must never fail
// because a cosmetic request did not arrive.
func (t typingRefresher) send(ctx context.Context) {
	if err := t.sender.SendChatAction(ctx, t.chatID, t.action); err != nil && t.logger != nil {
		t.logger.Printf("bridge: chat action: %v", err)
	}
}

// keepTypingAlive starts the typing refresher of one turn and returns the function
// that stops it.
func (r *bridgeRuntime) keepTypingAlive(ctx context.Context, chatID int64) (stop func()) {
	interval := r.typingInterval
	if interval <= 0 {
		interval = bridgeTypingInterval
	}
	return typingRefresher{
		sender: r.client,
		chatID: chatID,
		action: bridgeTypingAction,
		every:  interval,
		logger: r.logger,
	}.start(ctx)
}

// draftOwnership is the runtime's shared view of the one streaming draft. Every
// turn publishes into the same draft id, so a send replaces the previous draft
// instead of stacking up — cheap and idempotent — but it also means a placeholder
// published over answer text erases what the owner is reading until the next delta
// arrives. This state is what keeps that from happening: both sides claim the draft
// before they send it and release it after, and the claim of answer text is final
// for the placeholder.
type draftOwnership struct {
	mu sync.Mutex
	// text is true while a turn's streamed answer owns the draft.
	text bool
	// answer is the last answer text the turn published into the draft. A
	// placeholder that loses the draft while its send is in flight puts this text
	// back, so the owner never keeps reading the placeholder over the answer.
	answer string
}

// claimText takes the draft for the turn's streamed answer and records the text the
// draft now shows. It runs before the delta is sent, so the decision and the claim
// are one step: a placeholder that has not claimed yet never sends, and one already
// in flight finds the draft taken when it comes back.
func (d *draftOwnership) claimText(answer string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.text = true
	d.answer = answer
}

// releaseText ends the answer's ownership once the turn is over, so the next turn
// starts from a draft that carries no text.
func (d *draftOwnership) releaseText() {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.text = false
	d.answer = ""
}

// claimPlaceholder takes the draft for the thinking placeholder and reports whether
// it won. The claim is deliberately not held across the send — that would put a Bot
// API round trip inside the turn and serialise every delta behind it — so the caller
// releases it with releasePlaceholder once the send returns.
func (d *draftOwnership) claimPlaceholder() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return !d.text
}

// releasePlaceholder ends a placeholder's claim and reports the answer text the
// draft must show again when a delta took it while the placeholder was in flight.
// A placeholder that lost the draft is repaired with that text instead of being left
// on screen for the owner to read as the answer.
func (d *draftOwnership) releasePlaceholder() (answer string, lost bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if !d.text {
		return "", false
	}
	return d.answer, true
}

// bridgeDraftSender is the Bot API surface the thinking draft needs: one ephemeral
// draft that replaces the draft with the same id.
type bridgeDraftSender interface {
	SendRichDraft(ctx context.Context, chatID int64, draftID int, markdown string) error
}

// thinkingRefresher publishes the thinking placeholder into the one streaming
// draft. It is the only liveness signal Telegram Web renders for a bot, so a turn
// that has produced no text yet must keep republishing it — and it must never
// publish it over the answer.
type thinkingRefresher struct {
	sender  bridgeDraftSender
	chatID  int64
	draftID int
	text    string
	every   time.Duration
	logger  *log.Logger
	// owner is the shared draft state this refresher claims the draft from. A nil
	// owner means nobody competes for the draft (unit tests of the refresh itself);
	// otherwise every publish wins the claim before sending and releases it after.
	owner *draftOwnership
}

// publish emits one placeholder draft and reports whether the refresh may go on.
// A failed request is logged and never stops the refresh nor fails anything: the
// draft is cosmetic.
//
// The draft is claimed before the send and released after it, because the claim
// cannot be held across the Bot API call: a lock around a network round trip would
// serialise the turn's deltas behind it. The claim is what makes "may I publish the
// placeholder?" and "I own the draft" one step, so answer text that claims the
// draft after the decision keeps it. A placeholder that loses the claim while its
// send is in flight repairs the draft with the answer text at once, so the owner
// never reads the placeholder where the answer already was.
func (t thinkingRefresher) publish(ctx context.Context) bool {
	if t.owner != nil && !t.owner.claimPlaceholder() {
		return false
	}
	if err := t.sender.SendRichDraft(ctx, t.chatID, t.draftID, t.text); err != nil && t.logger != nil {
		t.logger.Printf("bridge: thinking draft: %v", err)
	}
	if t.owner == nil {
		return true
	}
	answer, lost := t.owner.releasePlaceholder()
	if !lost {
		return true
	}
	if answer != "" {
		if err := t.sender.SendRichDraft(ctx, t.chatID, t.draftID, answer); err != nil && t.logger != nil {
			t.logger.Printf("bridge: thinking draft repair: %v", err)
		}
	}
	return false
}

// start emits the placeholder at once — so a turn shows feedback before Pi is
// prompted — and repeats it every interval until stop is called, ctx ends, or a
// turn's answer takes the draft over. The returned stop is safe to call more than
// once and waits for the goroutine, so a finished turn leaves nothing behind; a
// cancelled ctx ends the goroutine by itself.
func (t thinkingRefresher) start(ctx context.Context) (stop func()) {
	t.publish(ctx)

	every := t.every
	if every <= 0 {
		every = bridgeThinkingInterval
	}

	stopCh := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(every)
		defer ticker.Stop()
		for {
			select {
			case <-stopCh:
				return
			case <-ctx.Done():
				return
			case <-ticker.C:
				if !t.publish(ctx) {
					return
				}
			}
		}
	}()

	var once sync.Once
	return func() {
		once.Do(func() { close(stopCh) })
		<-done
	}
}

// thinkingFor builds the thinking-draft publisher of one chat. Every path that
// publishes the placeholder — the arrival announcement, the resume announcement
// and the refresher of a running turn — goes through this one shape, so all of them
// share the draft id and the same "never over the answer" rule.
func (r *bridgeRuntime) thinkingFor(chatID int64) thinkingRefresher {
	return thinkingRefresher{
		sender:  r.client,
		chatID:  chatID,
		draftID: bridgeDraftID,
		text:    bridgeThinkingDraft,
		every:   r.thinkingInterval,
		logger:  r.logger,
		owner:   &r.draft,
	}
}

// announceThinking publishes the thinking draft outside the turn goroutine: when a
// message arrives (queued or not) and when an answer unblocks a dialogue. The send
// is bounded by bridgeAnnounceTimeout because these callers run in the poll loop,
// and a failure is logged only — feedback is cosmetic and must never fail a turn.
func (r *bridgeRuntime) announceThinking(ctx context.Context, chatID int64) {
	announceCtx, cancel := context.WithTimeout(ctx, bridgeAnnounceTimeout)
	defer cancel()
	r.thinkingFor(chatID).publish(announceCtx)
}

// keepThinkingAlive starts the thinking refresher of one turn and returns the
// function that stops it.
func (r *bridgeRuntime) keepThinkingAlive(ctx context.Context, chatID int64) (stop func()) {
	return r.thinkingFor(chatID).start(ctx)
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
