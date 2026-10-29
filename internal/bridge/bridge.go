// Package bridge drives one long-lived `pi --mode rpc` subprocess: the direct
// Telegram↔Pi channel of phase 1 (S1). Pi is the engine, so the bridge only
// writes prompts to stdin and reads protocol records from stdout.
//
// The process is spawned ONCE and reused across prompts with session
// persistence ON (no `--no-session`), so Pi remembers the conversation.
//
// Pi is a full gentle-shell, not a trimmed assistant (T11): its discovery stays
// enabled, so it loads the same global configuration as a terminal session
// (packages, extensions, skills, prompt templates, themes, context files). The
// bridge only ADDS: explicit `-e` extensions, persona files appended to the
// system prompt, and — when configured — a tool allowlist. With no allowlist
// configured Pi keeps its own default selection, which includes modifying tools
// (`bash`, `edit`, `write`).
//
// Pi works in Config.WorkDir (the repository in production, so project skills and
// project context are discovered from it). The vault is only the store the bridge
// tools read paths against, never the cwd.
package bridge

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"sort"
	"strings"
	"sync"
	"time"
)

// errProcessEnded reports that pi died before the run settled (stdout EOF
// instead of agent_settled). It is retryable: PromptStream restarts the process
// once and retries the same prompt.
var errProcessEnded = errors.New("bridge: pi process ended before settling")

// errEmptyResponse reports a settled run that produced no assistant text.
var errEmptyResponse = errors.New("bridge: pi returned an empty response")

// ErrTurnInFlight reports that Reset found a prompt owning the session, so the
// reset was refused instead of interrupting it.
var ErrTurnInFlight = errors.New("bridge: a prompt is in flight; the session was not reset")

// piMaxLine bounds a single RPC line read from stdout.
const piMaxLine = 8 * 1024 * 1024

// Config is the explicit configuration of the persistent Pi session.
type Config struct {
	// Bin is the pi CLI binary. Empty defaults to "pi" (resolved via PATH).
	Bin string
	// Provider and Model optionally pin the LLM (--provider/--model).
	Provider string
	Model    string
	// Timeout bounds one prompt round trip. When it fires the process is killed
	// so the next prompt restarts from a clean state. Non-positive: 60s.
	Timeout time.Duration
	// SessionID names the persistent session (--session-id).
	SessionID string
	// Tools is the optional pi tool allowlist (--tools). Empty, the default,
	// means "no restriction": the flag is not passed at all, so Pi uses its own
	// default selection from the global configuration, including modifying tools.
	// --tools REPLACES that selection, so setting this narrows the bridge on
	// purpose and is never a silent fallback.
	Tools string
	// Extensions are loaded explicitly with -e, one flag per path. They are
	// additive: extension discovery is NOT disabled, so the owner's global
	// packages load alongside these paths.
	Extensions []string
	// PromptFiles are appended with --append-system-prompt, one flag per file.
	PromptFiles []string
	// WorkDir is the working directory of the child process (cwd). Empty inherits
	// the bridge's own working directory. Production passes the configured
	// ALTER_BRIDGE_WORKDIR, which defaults to the process working directory: the
	// repository, so project skills in .agents/skills are discovered and never leak
	// into unrelated sessions. The vault path is not part of this struct: it belongs
	// to the tools server config, never to the child's cwd.
	WorkDir string
	// Env holds extra environment variables for the child process, applied on
	// top of the inherited environment. The bridge tools server hands Pi its URL
	// and token this way (ALTER_TOOLS_URL / ALTER_TOOLS_TOKEN). Keys are applied
	// in sorted order so the child environment is deterministic.
	Env map[string]string
	// OnDialog answers the blocking UI dialogs Pi sends (confirm, select, input,
	// editor). When nil every dialog is answered with `cancelled:true`, which Pi
	// turns into false/undefined: a headless session must never imply consent it
	// did not receive. An error or an expired context also cancels, so a handler
	// that cannot answer is indistinguishable from a refusal (T13, fail-closed).
	OnDialog func(ctx context.Context, req DialogRequest) (DialogResponse, error)
}

// DialogRequest is one blocking `extension_ui_request` Pi emitted. Only the dialog
// methods reach OnDialog: the fire-and-forget ones (notify, setStatus, setWidget,
// setTitle, set_editor_text) take no answer and are ignored.
type DialogRequest struct {
	// ID must be echoed back in the response; Pi matches the two.
	ID string
	// Method is "confirm", "select", "input" or "editor".
	Method string
	// Title and Message are the text Pi wants the user to read.
	Title   string
	Message string
	// Options are the choices of a "select" dialog, in the order Pi offered them.
	Options []string
	// Placeholder and Prefill belong to "input" and "editor".
	Placeholder string
	Prefill     string
}

// DialogResponse is the answer written back to Pi. Pi expects `confirmed` for
// confirm and `value` for select/input/editor; Cancelled dismisses any of them.
type DialogResponse struct {
	// Confirmed answers a "confirm" dialog.
	Confirmed bool
	// Value answers "select" (one of the offered options), "input" or "editor".
	Value string
	// Cancelled dismisses the dialog, so the extension receives false/undefined.
	Cancelled bool
}

// Option configures a Session. It exists mainly as the test seam.
type Option func(*Session)

// WithCommandFactory overrides the subprocess factory, so tests can run a fake
// Pi. No per-prompt context is ever attached to the spawned command: the
// process outlives a prompt and must not be torn down by one deadline.
func WithCommandFactory(f func(name string, args ...string) *exec.Cmd) Option {
	return func(s *Session) { s.newCmd = f }
}

// WithLogger sets the logger used for non-fatal diagnostics.
func WithLogger(l *log.Logger) Option {
	return func(s *Session) { s.logger = l }
}

// Session drives one persistent pi RPC process.
type Session struct {
	cfg    Config
	logger *log.Logger
	newCmd func(name string, args ...string) *exec.Cmd

	// mu serializes prompts and lifecycle changes: only one prompt is in flight
	// at a time, and start/restart/close never race a reader.
	mu sync.Mutex
	// cmd is the live process; nil while none is running. stdin, stdout and sc
	// belong to it. readerDone is closed by the goroutine currently reading
	// stdout, so killProcess can wait for the reader before reaping the child.
	cmd        *exec.Cmd
	stdin      io.WriteCloser
	stdout     io.Closer
	sc         *bufio.Scanner
	readerDone chan struct{}
	stderr     io.Writer
}

// errWriter forwards pi's stderr to the logger, which is safe for concurrent use.
type errWriter struct{ logger *log.Logger }

func (w errWriter) Write(p []byte) (int, error) {
	w.logger.Printf("bridge: pi stderr: %s", strings.TrimSpace(string(p)))
	return len(p), nil
}

// NewSession builds a Session with production defaults for Bin and Timeout.
func NewSession(cfg Config, opts ...Option) *Session {
	if cfg.Bin == "" {
		cfg.Bin = "pi"
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = 60 * time.Second
	}
	s := &Session{
		cfg:    cfg,
		logger: log.New(io.Discard, "", 0),
		newCmd: func(name string, args ...string) *exec.Cmd { return exec.Command(name, args...) },
	}
	for _, opt := range opts {
		opt(s)
	}
	s.stderr = errWriter{logger: s.logger}
	return s
}

// Prompt sends one message to the persistent session and returns the final
// assistant text.
func (s *Session) Prompt(ctx context.Context, text string) (string, error) {
	return s.PromptStream(ctx, text, nil)
}

// PromptStream is Prompt with streaming: onDelta receives the accumulated
// assistant text each time a text_delta arrives, so callers can publish a live
// draft. The returned text is the authoritative get_last_assistant_text value,
// not the accumulated draft.
//
// A prompt pi handles without starting a run returns "" with a nil error: there
// is no final text to publish, so the caller sends nothing rather than an empty
// message.
func (s *Session) PromptStream(ctx context.Context, text string, onDelta func(accumulated string)) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	// The per-turn budget starts once the prompt owns the session. Creating it
	// before the lock would make a prompt queued behind an in-flight one inherit
	// an already-consumed budget and kill the healthy process it just started.
	ctx, cancel := context.WithTimeout(ctx, s.cfg.Timeout)
	defer cancel()

	if err := s.ensureStarted(); err != nil {
		return "", err
	}

	final, handled, err := s.roundTrip(ctx, text, onDelta)
	// A process that died before settling is retryable: restart and retry once.
	if err != nil && errors.Is(err, errProcessEnded) && ctx.Err() == nil {
		if rerr := s.restart(); rerr != nil {
			return "", rerr
		}
		final, handled, err = s.roundTrip(ctx, text, onDelta)
	}
	if err != nil {
		return "", err
	}
	// A handled prompt starts no run, so an empty result is legitimate there.
	if final == "" && !handled {
		return "", errEmptyResponse
	}
	return final, nil
}

// Close shuts the process down. It is idempotent and safe before any start.
func (s *Session) Close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.killProcess()
}

// Reset starts a fresh conversation: it stops the current child process (reaped
// exactly as Close reaps it), forgets the session state and sets the session id
// the next spawn uses, so the next prompt begins a new transcript instead of
// resuming the old one.
//
// It is safe before any start and safe to call repeatedly. A reset never
// interrupts a prompt: while one owns the session Reset refuses at once with
// ErrTurnInFlight and changes nothing, so the state is never left half-reset and
// the run whose answer the caller is still waiting for is not killed. Refusing
// instead of queueing is deliberate: the queued command would wait for the whole
// turn, which may run for Config.Timeout. The caller retries once it has settled.
//
// ctx bounds the caller's own patience only: an already cancelled context is
// reported before anything is touched.
func (s *Session) Reset(ctx context.Context, sessionID string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	// TryLock is the whole in-flight check: PromptStream holds mu for the entire
	// turn, so a failed TryLock means a turn (or another reset) owns the session
	// right now. Nothing is touched in that case.
	if !s.mu.TryLock() {
		return ErrTurnInFlight
	}
	defer s.mu.Unlock()

	s.killProcess()
	s.cfg.SessionID = sessionID
	return nil
}

// ensureStarted starts the process only when none is running.
func (s *Session) ensureStarted() error {
	if s.sc != nil {
		return nil
	}
	return s.start()
}

// start spawns pi and wires stdin/stdout. The command is created directly (never
// with exec.CommandContext) so the process is not bound to one prompt's context.
func (s *Session) start() error {
	cmd := s.newCmd(s.cfg.Bin, s.args()...)
	// cwd = the configured work dir (the repository in production), so project
	// skills and project context resolve from the same place a terminal session
	// would see them.
	if s.cfg.WorkDir != "" {
		cmd.Dir = s.cfg.WorkDir
	}
	if env := s.childEnv(cmd.Env); env != nil {
		cmd.Env = env
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return fmt.Errorf("bridge: pi: stdin pipe: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("bridge: pi: stdout pipe: %w", err)
	}
	cmd.Stderr = s.stderr
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("bridge: pi: start: %w", err)
	}

	sc := bufio.NewScanner(stdout)
	sc.Buffer(make([]byte, 0, 64*1024), piMaxLine)

	s.cmd = cmd
	s.stdin = stdin
	s.stdout = stdout
	s.sc = sc
	return nil
}

// childEnv applies Config.Env on top of base. base is the environment the
// command already carries: nil (the production factory) means the inherited
// os.Environ(), so in production this is exactly append(os.Environ(), ...). A
// test factory that prepared its own Env keeps it instead of having it silently
// replaced. The extra keys are appended in sorted order, so the child
// environment never depends on Go's map iteration order.
func (s *Session) childEnv(base []string) []string {
	if len(s.cfg.Env) == 0 {
		return nil
	}
	keys := make([]string, 0, len(s.cfg.Env))
	for k := range s.cfg.Env {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	if base == nil {
		base = os.Environ()
	}
	env := make([]string, 0, len(base)+len(keys))
	env = append(env, base...)
	for _, k := range keys {
		env = append(env, k+"="+s.cfg.Env[k])
	}
	return env
}

// restart kills any running process and starts a fresh one.
func (s *Session) restart() error {
	s.killProcess()
	return s.start()
}

// killProcess terminates the process, reaps it exactly once and clears the
// session state. The caller must hold s.mu.
//
// Reaping is what releases the zombie and the parent-side pipes, so every
// termination path (timeout, restart, Close) goes through here: a long-lived
// daemon must not accumulate zombies or open descriptors. It also unblocks a
// reader waiting on a dead or hung stdout.
func (s *Session) killProcess() {
	if s.cmd == nil {
		return
	}
	// Closing stdin first releases a reader blocked writing a protocol record to
	// a pi that stopped reading.
	if s.stdin != nil {
		_ = s.stdin.Close()
	}
	// Closing the read side releases a reader blocked on stdout. The reader must
	// be gone before cmd.Wait runs: Wait closes the same pipe, so waiting first
	// keeps the two from racing and losing buffered records.
	if s.stdout != nil {
		_ = s.stdout.Close()
	}
	if s.readerDone != nil {
		<-s.readerDone
	}
	if s.cmd.Process != nil {
		// Safe when the child already exited and was reaped: Kill then reports
		// os.ErrProcessDone and changes nothing.
		_ = s.cmd.Process.Kill()
	}
	// cmd.Wait is called once per spawned process: it reaps the child and closes
	// the parent-side pipes.
	_ = s.cmd.Wait()

	s.cmd = nil
	s.stdin = nil
	s.stdout = nil
	s.sc = nil
	s.readerDone = nil
}

// args builds the pi CLI arguments: RPC mode, the pinned provider/model, the
// explicitly configured extensions, persona files and session, and — only when
// configured — a tool allowlist.
//
// Nothing disables discovery: Pi loads the same global configuration it loads in
// a terminal (packages, extensions, skills, prompt templates, themes, context
// files), so the bridge is a full gentle-shell. The -e and
// --append-system-prompt entries are additive on top of it.
func (s *Session) args() []string {
	args := []string{"--mode", "rpc"}
	if s.cfg.Provider != "" {
		args = append(args, "--provider", s.cfg.Provider)
	}
	if s.cfg.Model != "" {
		args = append(args, "--model", s.cfg.Model)
	}
	// --no-approve is NOT passed: it disables project-file trust, which has
	// nothing to do with per-command permission. Destructive tool calls are gated
	// by the tools extension through Config.OnDialog instead (T13).
	// Session persistence stays ON: --no-session is never passed.
	for _, ext := range s.cfg.Extensions {
		args = append(args, "-e", ext)
	}
	// Empty Tools means "no restriction": omitting the flag keeps Pi's own
	// selection instead of replacing it with an allowlist.
	if s.cfg.Tools != "" {
		args = append(args, "--tools", s.cfg.Tools)
	}
	if s.cfg.SessionID != "" {
		args = append(args, "--session-id", s.cfg.SessionID)
	}
	for _, file := range s.cfg.PromptFiles {
		args = append(args, "--append-system-prompt", file)
	}
	return args
}

// roundTrip sends one prompt and reads the event stream until the run is
// decided. Reads run in a goroutine so the per-prompt context can still fire
// and kill the process when pi hangs.
func (s *Session) roundTrip(ctx context.Context, text string, onDelta func(string)) (string, bool, error) {
	stdin := s.stdin
	if err := writeJSON(stdin, promptCommand(text)); err != nil {
		return "", false, fmt.Errorf("bridge: pi: write prompt: %w", err)
	}

	type result struct {
		text    string
		handled bool
		err     error
	}
	ch := make(chan result, 1)
	sc := s.sc // pinned: a restart must never swap the scanner under the reader
	// Publish the reader's lifetime so killProcess can wait for it before it
	// reaps the child.
	readerDone := make(chan struct{})
	s.readerDone = readerDone
	go func() {
		defer close(readerDone)
		st := runState{delta: onDelta}
		for sc.Scan() {
			settled, err := s.handleLine(ctx, sc.Bytes(), &st, stdin)
			if err != nil {
				ch <- result{err: err}
				return
			}
			if settled {
				ch <- result{text: st.text, handled: st.handled, err: st.err}
				return
			}
		}
		if err := sc.Err(); err != nil && !errors.Is(err, io.EOF) {
			ch <- result{err: fmt.Errorf("bridge: pi: read events: %w", err)}
			return
		}
		// EOF with no terminal record: the process died mid-run.
		ch <- result{err: errProcessEnded}
	}()

	select {
	case <-ctx.Done():
		// Timeout or cancellation: kill the process so the reader unblocks and
		// the next prompt restarts cleanly.
		s.killProcess()
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return "", false, fmt.Errorf("bridge: pi: timed out after %s", s.cfg.Timeout)
		}
		return "", false, ctx.Err()
	case r := <-ch:
		return r.text, r.handled, r.err
	}
}

// runState accumulates the protocol facts handleLine extracts for one prompt.
type runState struct {
	// err is a decided run failure (rejected prompt, failed final-text query).
	err error
	// handled is set when pi consumed the prompt without starting a run.
	handled bool
	// text is the authoritative final assistant text.
	text string
	// draft is the accumulated streamed text, pushed to delta on every change.
	draft string
	delta func(string)
}

// handleLine processes one RPC record. It returns done = true when the outcome
// of the run is decided, and a non-nil error on a hard protocol/IO failure. ctx is
// the turn's context: a dialog answered after it expires is cancelled, never
// accepted.
func (s *Session) handleLine(ctx context.Context, line []byte, st *runState, stdin io.Writer) (bool, error) {
	var ev rpcEvent
	if err := json.Unmarshal(line, &ev); err != nil {
		s.logger.Printf("bridge: skipping malformed RPC line: %v", err)
		return false, nil
	}

	switch ev.Type {
	case "response":
		switch ev.Command {
		case "prompt":
			if !success(ev) {
				st.err = fmt.Errorf("bridge: pi rejected the prompt: %s", nonEmpty(ev.Error, "unknown error"))
				return true, nil
			}
			var data struct {
				Disposition string `json:"disposition"`
			}
			if err := json.Unmarshal(ev.Data, &data); err == nil && data.Disposition == "handled" {
				// No run started: waiting for agent_settled would hang forever. The
				// run is decided with an explicitly empty text. The final text only
				// ever comes from get_last_assistant_text, so the streamed draft is
				// never promoted to it; handled tells PromptStream that empty means
				// "nothing to publish" instead of an empty response error, leaving
				// the caller free not to send a message.
				st.handled = true
				st.text = ""
				return true, nil
			}
		case "get_last_assistant_text":
			if !success(ev) {
				st.err = fmt.Errorf("bridge: get_last_assistant_text failed: %s", nonEmpty(ev.Error, "unknown error"))
				return true, nil
			}
			var data struct {
				Text *string `json:"text"`
			}
			if err := json.Unmarshal(ev.Data, &data); err != nil {
				st.err = fmt.Errorf("bridge: decode get_last_assistant_text: %w", err)
				return true, nil
			}
			if data.Text != nil {
				st.text = *data.Text
			}
			return true, nil
		}

	case "agent_settled":
		// agent_settled — not agent_end — is the real end of the run: no retry,
		// compaction continuation or queued work follows. Only now is the final
		// assistant text meaningful.
		if err := writeJSON(stdin, lastAssistantTextCommand()); err != nil {
			return false, fmt.Errorf("bridge: pi: write get_last_assistant_text: %w", err)
		}

	case "message_update":
		// Partial assistant text: accumulate deltas and publish the draft.
		var update struct {
			Update *struct {
				Type  string `json:"type"`
				Delta string `json:"delta"`
			} `json:"assistantMessageEvent"`
		}
		if err := json.Unmarshal(line, &update); err == nil && update.Update != nil &&
			update.Update.Type == "text_delta" && update.Update.Delta != "" {
			st.draft += update.Update.Delta
			if st.delta != nil {
				st.delta(st.draft)
			}
		}

	case "extension_ui_request":
		// A dialog blocks the extension until it is answered, so it must always get
		// exactly one response: either the caller's answer or a cancellation.
		return false, s.answerDialog(ctx, ev, stdin)

	case "extension_error":
		s.logger.Printf("bridge: extension error: %s", ev.Error)
	}
	return false, nil
}

// answerDialog answers one extension UI request. Fire-and-forget methods are
// ignored; every dialog path that is not a real answer writes `cancelled:true`.
func (s *Session) answerDialog(ctx context.Context, ev rpcEvent, stdin io.Writer) error {
	if !isDialogMethod(ev.Method) {
		return nil
	}
	if s.cfg.OnDialog == nil {
		s.logger.Printf("bridge: dialog %q (%s) with no handler configured; answering cancelled", ev.Method, ev.ID)
		return s.writeDialogResponse(stdin, ev.Method, ev.ID, DialogResponse{Cancelled: true})
	}

	resp, err := s.cfg.OnDialog(ctx, DialogRequest{
		ID:          ev.ID,
		Method:      ev.Method,
		Title:       ev.Title,
		Message:     ev.dialogText(),
		Options:     ev.Options,
		Placeholder: ev.Placeholder,
		Prefill:     ev.Prefill,
	})
	// Fail closed: a failed handler, an expired turn or an explicit cancellation
	// all become cancelled. A late "yes" after the turn is over must never
	// authorise anything.
	switch {
	case err != nil:
		s.logger.Printf("bridge: dialog %q (%s) failed: %v; answering cancelled", ev.Method, ev.ID, err)
		resp = DialogResponse{Cancelled: true}
	case ctx.Err() != nil:
		s.logger.Printf("bridge: dialog %q (%s) outlived the turn (%v); answering cancelled", ev.Method, ev.ID, ctx.Err())
		resp = DialogResponse{Cancelled: true}
	}
	return s.writeDialogResponse(stdin, ev.Method, ev.ID, resp)
}

// writeDialogResponse writes the one record Pi is waiting for. confirm carries a
// boolean, the other dialog methods carry a value, and a cancellation carries
// neither.
func (s *Session) writeDialogResponse(stdin io.Writer, method, id string, resp DialogResponse) error {
	record := map[string]any{"type": "extension_ui_response", "id": id}
	switch {
	case resp.Cancelled:
		record["cancelled"] = true
	case method == "confirm":
		record["confirmed"] = resp.Confirmed
	default:
		// select, input and editor all answer with a value.
		record["value"] = resp.Value
	}
	if err := writeJSON(stdin, record); err != nil {
		return fmt.Errorf("bridge: pi: write extension_ui_response: %w", err)
	}
	return nil
}

// isDialogMethod reports whether the method blocks until the client answers.
func isDialogMethod(method string) bool {
	switch method {
	case "confirm", "select", "input", "editor":
		return true
	}
	return false
}

// rpcEvent is the envelope shared by the RPC records this package reads. The UI
// fields are only set for extension_ui_request records.
type rpcEvent struct {
	Type    string          `json:"type"`
	Command string          `json:"command"`
	Success *bool           `json:"success"`
	Error   string          `json:"error"`
	Data    json.RawMessage `json:"data"`

	ID          string          `json:"id"`
	Method      string          `json:"method"`
	Title       string          `json:"title"`
	Message     json.RawMessage `json:"message"`
	Options     []string        `json:"options"`
	Placeholder string          `json:"placeholder"`
	Prefill     string          `json:"prefill"`
}

// dialogText decodes the message field of a dialog request. extension_ui_request
// carries a plain string there, while model events such as message_end and
// turn_end carry an object. Decoding into a string field made the whole record
// fail to unmarshal, so those events were dropped and logged as malformed.
func (ev rpcEvent) dialogText() string {
	if len(ev.Message) == 0 {
		return ""
	}
	var text string
	if err := json.Unmarshal(ev.Message, &text); err != nil {
		return ""
	}
	return text
}

// success reports ev.Success, defaulting to true when absent.
func success(ev rpcEvent) bool {
	return ev.Success == nil || *ev.Success
}

func promptCommand(text string) map[string]any {
	return map[string]any{"id": "alter-bridge-prompt", "type": "prompt", "message": text}
}

func lastAssistantTextCommand() map[string]any {
	return map[string]any{"id": "alter-bridge-text", "type": "get_last_assistant_text"}
}

// writeJSON writes one JSONL record and flushes it immediately.
func writeJSON(w io.Writer, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(w, "%s\n", b)
	return err
}

func nonEmpty(s, def string) string {
	if s != "" {
		return s
	}
	return def
}
