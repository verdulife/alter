// Package bridge implements the direct Telegram→Pi communication path: one
// long-lived `pi --mode rpc` subprocess that keeps its conversation (session
// persistence ON) and is driven per Telegram message with a single `prompt`
// RPC command. It is the M1 spike of the bridge design (docs/bridge-telegram-pi.md):
//
//   - persistence: the process is spawned once and reused across prompts (no
//     `--no-session`, no respawn per message), so Pi remembers the thread.
//   - clean load: the process is started with discovery disabled (`--no-extensions`,
//     `--no-skills`, `--no-prompt-templates`, `--no-themes`, `--no-context-files`,
//     `--no-approve`) and without tools (`--no-tools`). It loads nothing extra.
//   - one user / one chat (D7): the session is a single sequential flow; access
//     is serialized so only one prompt is in flight at a time.
//
// This package never spawns the old one-shot adapter and does not use the
// AgentFlow/CapabilityFlow pipeline: it is the future replacement for the
// orchestrator, talking to Pi directly.
package bridge

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"os/exec"
	"sync"
	"time"

	"github.com/verdu/alter/internal/domain"
)

// errProcessEnded signals that the pi process died before the run settled (EOF
// on stdout). It is retryable: the Session restarts the process once.
var errProcessEnded = errors.New("bridge: pi process ended before settling")

// Config is the explicit configuration of the persistent bridge session.
type Config struct {
	// Bin is the pi CLI binary path. Empty defaults to "pi" (resolved via PATH).
	Bin string
	// Provider optionally overrides the LLM provider (--provider).
	Provider string
	// Model optionally overrides the model (--model, "provider/id[:thinking]").
	Model string
	// Timeout bounds one prompt round-trip (send + LLM run + final text). A
	// prompt that exceeds it is aborted and the pi process is restarted so the
	// session returns to a clean state. Non-positive defaults to 60s.
	Timeout time.Duration
	// SessionName optionally names the persistent pi session (--name).
	SessionName string
}

// Option configures a Session (testing seam and diagnostics).
type Option func(*Session)

// WithCommandFactory overrides the subprocess factory (deterministic tests: the
// "binary" is the test binary itself re-executed as an RPC fake). The factory
// is used only to spawn the process; Session never passes a per-prompt context
// to it (the process outlives a prompt), so the persistent process is not torn
// down when one prompt's timeout fires.
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

	// mu serializes access to the single process: only one prompt is in flight,
	// and lifecycle changes (start/restart/close) never race a reader.
	mu     sync.Mutex
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	sc     *bufio.Scanner
	stderr bytes.Buffer
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
	for _, o := range opts {
		o(s)
	}
	return s
}

// Prompt sends one message to the persistent pi session and returns the final
// assistant text. It serializes access and, if the process died mid-run,
// restarts it once and retries the prompt.
func (s *Session) Prompt(ctx context.Context, instruction string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, s.cfg.Timeout)
	defer cancel()

	s.mu.Lock()
	defer s.mu.Unlock()

	if err := s.ensureStarted(ctx); err != nil {
		return "", err
	}

	text, err := s.roundTrip(ctx, instruction)
	// A process that died before settling is retryable: restart and retry once.
	if err != nil && errors.Is(err, errProcessEnded) {
		if ctx.Err() != nil {
			// Already timed out or cancelled: no point restarting, report as-is.
			return "", err
		}
		if rerr := s.restart(ctx); rerr != nil {
			return "", rerr
		}
		text, err = s.roundTrip(ctx, instruction)
	}
	if err != nil {
		return "", err
	}
	if text == "" {
		// A settled run must carry a response; never deliver empty.
		return "", classify(domain.AgentErrorKindEmptyResponse, errors.New("bridge: pi returned an empty response"))
	}
	return text, nil
}

// Handle matches the telegram.NaturalHandler signature (ctx, text -> reply),
// so a Session can be wired directly as the inbound free-text handler.
func (s *Session) Handle(ctx context.Context, text string) (string, error) {
	return s.Prompt(ctx, text)
}

// Close terminates the pi process and releases its resources. It is idempotent
// and safe to call when the process was never started.
func (s *Session) Close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.killProcess()
}

// ensureStarted starts the process only if it is not already running.
func (s *Session) ensureStarted(ctx context.Context) error {
	if s.sc != nil {
		return nil
	}
	return s.start(ctx)
}

// start spawns the pi process and wires stdin/stdout/scanner. The process is
// created directly (not with exec.CommandContext) so it is NOT tied to any
// single prompt's context: it lives until Close or a timeout-driven restart.
func (s *Session) start(ctx context.Context) error {
	s.stderr.Reset()
	cmd := s.newCmd(s.cfg.Bin, s.args()...)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return classify(domain.AgentErrorKindInternal, fmt.Errorf("bridge: pi: stdin pipe: %w", err))
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return classify(domain.AgentErrorKindInternal, fmt.Errorf("bridge: pi: stdout pipe: %w", err))
	}
	cmd.Stderr = &s.stderr

	if err := cmd.Start(); err != nil {
		if errors.Is(err, exec.ErrNotFound) || errors.Is(err, fs.ErrNotExist) {
			// The binary does not exist: an environment misconfiguration an
			// operator must fix. Retrying is useless.
			return classify(domain.AgentErrorKindProvider, fmt.Errorf("%w: bridge: pi: %v", domain.ErrActionPermanent, err))
		}
		return classify(domain.AgentErrorKindInternal, fmt.Errorf("bridge: pi: start: %w", err))
	}

	sc := bufio.NewScanner(stdout)
	sc.Buffer(make([]byte, 0, 64*1024), piMaxLine)

	s.cmd = cmd
	s.stdin = stdin
	s.sc = sc
	return nil
}

// restart kills any running process and starts a fresh one.
func (s *Session) restart(ctx context.Context) error {
	s.killProcess()
	return s.start(ctx)
}

// killProcess terminates the process and clears the session state. It also
// unblocks any reader goroutine blocked on a dead/hung stdout.
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

// args builds the pi CLI arguments for the persistent clean session.
func (s *Session) args() []string {
	args := []string{"--mode", "rpc"}
	if s.cfg.Provider != "" {
		args = append(args, "--provider", s.cfg.Provider)
	}
	if s.cfg.Model != "" {
		args = append(args, "--model", s.cfg.Model)
	}
	if s.cfg.SessionName != "" {
		args = append(args, "--name", s.cfg.SessionName)
	}
	// Clean load + persistence ON (no --no-session). M1 carries no tools yet;
	// M2 will swap --no-tools for a --tools allowlist.
	args = append(args,
		"--no-tools",
		"--no-extensions",
		"--no-skills",
		"--no-prompt-templates",
		"--no-themes",
		"--no-context-files",
		"--no-approve",
	)
	return args
}

// roundTrip sends one prompt and reads stdout until the run settles and the
// final assistant text is returned. It is tolerant of a hung process: reads run
// in a goroutine so the per-prompt context can abort and trigger a restart.
func (s *Session) roundTrip(ctx context.Context, instruction string) (string, error) {
	if err := writeJSON(s.stdin, promptCommand(instruction)); err != nil {
		return "", classify(domain.AgentErrorKindInternal, fmt.Errorf("bridge: pi: write prompt: %w", err))
	}

	type result struct {
		text string
		err  error
	}
	ch := make(chan result, 1)
	sc := s.sc // pinned: the reader must never touch a reassigned (restarted) scanner
	go func() {
		var st runState
		for sc.Scan() {
			done, err := s.handleLine(sc.Bytes(), &st)
			if err != nil {
				ch <- result{err: err}
				return
			}
			if done {
				text, rerr := finishRound(&st)
				ch <- result{text: text, err: rerr}
				return
			}
		}
		if err := sc.Err(); err != nil && !errors.Is(err, io.EOF) {
			ch <- result{err: err}
			return
		}
		// EOF with no terminal response: the process died mid-run.
		ch <- result{err: errProcessEnded}
	}()

	select {
	case <-ctx.Done():
		// Timeout/cancel: kill the process so the reader goroutine unblocks on
		// the dead stdout, and the next Prompt restarts cleanly.
		s.killProcess()
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return "", classify(domain.AgentErrorKindTimeout, fmt.Errorf("bridge: pi: timed out after %s", s.cfg.Timeout))
		}
		return "", classify(domain.AgentErrorKindInternal, ctx.Err())
	case r := <-ch:
		if r.err != nil {
			if errors.Is(r.err, errProcessEnded) {
				return "", r.err
			}
			if isClassified(r.err) {
				return "", r.err
			}
			return "", classify(domain.AgentErrorKindInternal, r.err)
		}
		return r.text, nil
	}
}

// runState accumulates the protocol facts handleLine extracts from events.
type runState struct {
	promptRejected error
	runnerErr      error
	gotText        bool
	text           string
}

// handleLine processes one RPC event line. It returns done=true when the run
// outcome is decided and no further lines are needed. A non-nil error is a hard
// protocol/IO failure. Prompt rejection is recorded in st (classified permanent
// by finishRound).
func (s *Session) handleLine(line []byte, st *runState) (bool, error) {
	var ev rpcEvent
	if err := json.Unmarshal(line, &ev); err != nil {
		s.logger.Printf("bridge: skip malformed line: %v", err)
		return false, nil
	}

	switch ev.Type {
	case "response":
		switch ev.Command {
		case "prompt":
			if ev.Success != nil && !*ev.Success {
				st.promptRejected = errors.New(nonEmpty(ev.Error, "prompt rejected before acceptance"))
				return true, nil
			}
		case "get_last_assistant_text":
			if ev.Success != nil && !*ev.Success {
				st.runnerErr = errors.New(nonEmpty(ev.Error, "get_last_assistant_text failed"))
				return true, nil
			}
			var data struct {
				Text *string `json:"text"`
			}
			if err := json.Unmarshal(ev.Data, &data); err != nil {
				st.runnerErr = fmt.Errorf("decode response data: %w", err)
				return true, nil
			}
			st.gotText = true
			if data.Text != nil {
				st.text = *data.Text
			}
			return true, nil

		default:
			// Other command responses (set_model, get_state, ...) never occur in
			// this flow; ignore.
		}

	case "agent_settled":
		// Only now is the final text meaningful (no automatic retry, compaction
		// retry, or queued continuation remains).
		if st.runnerErr != nil {
			return true, nil
		}
		if !st.gotText {
			if err := writeJSON(s.stdin, getLastAssistantText()); err != nil {
				return false, fmt.Errorf("bridge: pi: write get_last_assistant_text: %w", err)
			}
		}

	case "auto_retry_end":
		// Pi retried transient provider errors internally. A failed retry ends
		// the run: record it so we return retryable and skip the final text.
		if ev.Success != nil && !*ev.Success {
			st.runnerErr = errors.New(nonEmpty(ev.FinalError, "provider error after retries"))
		}

	case "extension_ui_request":
		// The bridge loads no extensions, so this should not occur; ignore
		// defensively rather than hang on a dialog.
	case "extension_error":
		s.logger.Printf("bridge: extension error: %v", ev.Error)
	}

	return false, nil
}

// finishRound classifies the collected outcome and returns the final text.
func finishRound(st *runState) (string, error) {
	switch {
	case st.promptRejected != nil:
		return "", classify(domain.AgentErrorKindProvider, fmt.Errorf("%w: bridge: pi: %v", domain.ErrActionPermanent, st.promptRejected))
	case st.runnerErr != nil:
		return "", classify(domain.AgentErrorKindProvider, fmt.Errorf("bridge: pi: agent failed: %w", st.runnerErr))
	default:
		return st.text, nil
	}
}

// rpcEvent is the minimal envelope shared by every RPC line. Only the fields
// this package reads are decoded.
type rpcEvent struct {
	Type       string          `json:"type"`
	Command    string          `json:"command"`
	Success    *bool           `json:"success"`
	Error      string          `json:"error"`
	FinalError string          `json:"finalError"`
	Data       json.RawMessage `json:"data"`
	ID         string          `json:"id"`
	Method     string          `json:"method"`
}

func promptCommand(instruction string) map[string]any {
	return map[string]any{"id": "alter-bridge-prompt", "type": "prompt", "message": instruction}
}

func getLastAssistantText() map[string]any {
	return map[string]any{"id": "alter-bridge-text", "type": "get_last_assistant_text"}
}

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

func isClassified(err error) bool {
	var ae *domain.AgentError
	return errors.As(err, &ae)
}

// classify attaches a domain.AgentError classification to err, keeping the
// original error text and its wrapping chain.
func classify(kind domain.AgentErrorKind, err error) error {
	return &domain.AgentError{Kind: kind, Err: err}
}

// piMaxLine is the maximum length of a single RPC line.
const piMaxLine = 8 * 1024 * 1024