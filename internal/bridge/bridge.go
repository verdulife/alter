// Package bridge drives one long-lived `pi --mode rpc` subprocess: the direct
// Telegram↔Pi channel of phase 1 (S1). Pi is the engine, so the bridge only
// writes prompts to stdin and reads protocol records from stdout.
//
// The process is spawned ONCE and reused across prompts with session
// persistence ON (no `--no-session`), so Pi remembers the conversation. The load
// is clean (discovery off); only explicitly configured extensions, the tool
// allowlist and the system-prompt files are pulled in.
//
// Pi does not write (S7): it runs with cwd = vault root and the read-only tool
// set (read, grep, find, ls). Every mutation belongs to deterministic tools
// owned by the bridge, never to the model.
package bridge

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os/exec"
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
	// Tools is the pi tool allowlist (--tools). Empty falls back to
	// defaultReadOnlyTools: Pi must never receive its default tool set, which
	// includes write tools (S7).
	Tools string
	// Extensions are loaded explicitly with -e, one flag per path. Explicit
	// paths still load while --no-extensions keeps discovery disabled.
	Extensions []string
	// PromptFiles are appended with --append-system-prompt, one flag per file.
	PromptFiles []string
	// VaultDir is the working directory of the process: cwd = vault root is how
	// Pi reads the vault while staying read-only.
	VaultDir string
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
	mu     sync.Mutex
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	sc     *bufio.Scanner
	stderr io.Writer
}

// errWriter forwards pi's stderr to the logger, which is safe for concurrent use.
type errWriter struct{ logger *log.Logger }

func (w errWriter) Write(p []byte) (int, error) {
	w.logger.Printf("bridge: pi stderr: %s", strings.TrimSpace(string(p)))
	return len(p), nil
}

// NewSession builds a Session with production defaults for Bin and Timeout.
// defaultReadOnlyTools is the allowlist applied when Config.Tools is empty. Pi
// reads the vault with these four and mutates nothing directly; writes go
// through the deterministic extension tools.
const defaultReadOnlyTools = "read,grep,find,ls"

func NewSession(cfg Config, opts ...Option) *Session {
	if cfg.Bin == "" {
		cfg.Bin = "pi"
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = 60 * time.Second
	}
	if cfg.Tools == "" {
		cfg.Tools = defaultReadOnlyTools
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
func (s *Session) PromptStream(ctx context.Context, text string, onDelta func(accumulated string)) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, s.cfg.Timeout)
	defer cancel()

	s.mu.Lock()
	defer s.mu.Unlock()

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
	// cwd = vault root (S7): Pi reads the vault through its read-only tools.
	if s.cfg.VaultDir != "" {
		cmd.Dir = s.cfg.VaultDir
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
	s.sc = sc
	return nil
}

// restart kills any running process and starts a fresh one.
func (s *Session) restart() error {
	s.killProcess()
	return s.start()
}

// killProcess terminates the process and clears the session state. The caller
// must hold s.mu. It also unblocks a reader waiting on a dead or hung stdout.
func (s *Session) killProcess() {
	if s.stdin != nil {
		_ = s.stdin.Close()
	}
	if s.cmd != nil && s.cmd.Process != nil {
		_ = s.cmd.Process.Kill()
	}
	s.cmd = nil
	s.stdin = nil
	s.sc = nil
}

// args builds the pi CLI arguments: clean load, explicit extensions, the tool
// allowlist, the named persistent session and the persona files.
func (s *Session) args() []string {
	args := []string{"--mode", "rpc"}
	if s.cfg.Provider != "" {
		args = append(args, "--provider", s.cfg.Provider)
	}
	if s.cfg.Model != "" {
		args = append(args, "--model", s.cfg.Model)
	}
	// Clean load. Session persistence stays ON: --no-session is never passed.
	args = append(args,
		"--no-extensions",
		"--no-skills",
		"--no-prompt-templates",
		"--no-themes",
		"--no-context-files",
		"--no-approve",
	)
	for _, ext := range s.cfg.Extensions {
		args = append(args, "-e", ext)
	}
	// NewSession always resolves an allowlist, so this is never empty.
	args = append(args, "--tools", s.cfg.Tools)
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
	go func() {
		st := runState{delta: onDelta}
		for sc.Scan() {
			done, err := s.handleLine(sc.Bytes(), &st, stdin)
			if err != nil {
				ch <- result{err: err}
				return
			}
			if done {
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
// of the run is decided, and a non-nil error on a hard protocol/IO failure.
func (s *Session) handleLine(line []byte, st *runState, stdin io.Writer) (bool, error) {
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
				// No run started: waiting for agent_settled would hang forever.
				st.handled = true
				st.text = st.draft
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

	case "extension_error":
		s.logger.Printf("bridge: extension error: %s", ev.Error)
	}
	return false, nil
}

// rpcEvent is the envelope shared by the RPC records this package reads.
type rpcEvent struct {
	Type    string          `json:"type"`
	Command string          `json:"command"`
	Success *bool           `json:"success"`
	Error   string          `json:"error"`
	Data    json.RawMessage `json:"data"`
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
