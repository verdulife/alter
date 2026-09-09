package agent

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
	"time"

	"github.com/verdu/alter/internal/domain"
)

// PiAgent implements domain.Agent by driving the pi CLI RPC mode as a one-shot
// subprocess per execution: spawn `pi --mode rpc --no-session`, send one
// prompt, wait for the run to settle, ask for the final assistant text, then
// terminate the process. It never keeps a persistent Pi process for V1.
//
// Dependency direction: this file depends only on internal/domain and the
// standard library. The wire contract is the documented pi RPC protocol
// (JSONL over stdin/stdout; see docs/pi-agent-rpc.md and the installed pi
// package's docs/rpc.md).
//
// Error classification happens at this boundary, per domain.Agent:
//   - permanent (wrapped with domain.ErrActionPermanent): the pi binary is not
//     found, or Pi rejected the prompt before acceptance (response
//     command=prompt success=false). Retrying would never succeed: it is an
//     environment/configuration/contract problem. The Scheduler retires the
//     trigger.
//   - everything else is retryable by default: spawn/IO failures, process
//     crashes before agent_settled, timeouts, provider transient failures after
//     Pi exhausted its internal retries, and runs that settle without a
//     response text. The Scheduler applies its RetryAt backoff.
type PiAgent struct {
	cfg    PiConfig
	logger *log.Logger
	// newCmd is the process factory seam for deterministic tests; production
	// uses exec.CommandContext (kills the subprocess when ctx is done).
	newCmd func(ctx context.Context, name string, args ...string) *exec.Cmd
}

var _ domain.Agent = (*PiAgent)(nil)

// PiConfig is the explicit configuration of the Pi RPC adapter.
type PiConfig struct {
	// Bin is the pi CLI binary path. Empty defaults to "pi" (resolved via
	// PATH); a shell-independent deployment should pass an absolute path.
	Bin string
	// Provider optionally overrides the LLM provider (--provider). Empty lets
	// Pi use its own configured/default provider.
	Provider string
	// Model optionally overrides the model (--model, "provider/id[:thinking]").
	// Empty lets Pi use its own configured/default model.
	Model string
	// Timeout bounds one whole execution (spawn + LLM run + response).
	// Non-positive defaults to 60s.
	Timeout time.Duration
	// NoTools passes --no-tools: the agent cannot call tools and only produces
	// user-facing text. The safe V1 default is true; the runtime config layer
	// owns that default and an explicit override re-enables tools.
	NoTools bool
	// SystemPrompt is appended via --append-system-prompt when non-empty.
	SystemPrompt string
}

// PiOption configures a PiAgent (testing seam and diagnostics).
type PiOption func(*PiAgent)

// WithCommandFactory overrides the subprocess factory (deterministic tests:
// the "binary" is the test binary itself re-executed as an RPC fake).
func WithCommandFactory(f func(ctx context.Context, name string, args ...string) *exec.Cmd) PiOption {
	return func(a *PiAgent) { a.newCmd = f }
}

// WithPiLogger sets the logger used for non-fatal diagnostics. It is named
// WithPiLogger (not WithLogger) because the Orchestrator in this package
// already owns WithLogger for its own Option type.
func WithPiLogger(l *log.Logger) PiOption {
	return func(a *PiAgent) { a.logger = l }
}

// NewPiAgent builds a PiAgent with production defaults for Bin and Timeout.
func NewPiAgent(cfg PiConfig, opts ...PiOption) *PiAgent {
	if cfg.Bin == "" {
		cfg.Bin = "pi"
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = 60 * time.Second
	}
	a := &PiAgent{
		cfg:    cfg,
		logger: log.New(io.Discard, "", 0),
		newCmd: exec.CommandContext,
	}
	for _, o := range opts {
		o(a)
	}
	return a
}

// Execute runs one Pi RPC one-shot execution and returns the final assistant
// text as the user-facing response.
func (a *PiAgent) Execute(ctx context.Context, request domain.AgentRequest) (domain.AgentResult, error) {
	ctx, cancel := context.WithTimeout(ctx, a.cfg.Timeout)
	defer cancel()

	cmd := a.newCmd(ctx, a.cfg.Bin, a.args()...)

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return domain.AgentResult{}, fmt.Errorf("pi: stdin pipe: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return domain.AgentResult{}, fmt.Errorf("pi: stdout pipe: %w", err)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	if err := cmd.Start(); err != nil {
		// exec.ErrNotFound covers relative names failed by LookPath; an absolute
		// path fails with a wrapped ENOENT instead. Both mean the binary does not
		// exist: an environment misconfiguration an operator must fix (e.g. set
		// ALTER_PI_BIN). Retrying is useless, so the trigger is retired.
		if errors.Is(err, exec.ErrNotFound) || errors.Is(err, fs.ErrNotExist) {
			return domain.AgentResult{}, fmt.Errorf("%w: pi: %v", domain.ErrActionPermanent, err)
		}
		return domain.AgentResult{}, fmt.Errorf("pi: start: %w", err)
	}

	text, runErr := a.run(ctx, stdin, stdout, request.Instruction)

	// Always terminate and reap: close stdin so Pi exits cleanly (observed: RPC
	// mode exits 0 on stdin EOF), with a bounded grace so a Pi version that
	// ignores stdin close cannot hang the caller. The Wait error is diagnostic
	// only: the run outcome was already decided above.
	_ = stdin.Close()
	reapCtx, reapCancel := context.WithTimeout(context.Background(), piReapGrace)
	defer reapCancel()
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case <-done:
	case <-reapCtx.Done():
		_ = cmd.Process.Kill()
		<-done
	}

	if runErr != nil {
		if stderr.Len() > 0 {
			a.logger.Printf("pi: stderr: %s", boundedText(stderr.String(), 4096))
		}
		return domain.AgentResult{}, runErr
	}
	if text == "" {
		// Defensive: a settled run must carry a response; never deliver empty.
		return domain.AgentResult{}, errors.New("pi: agent returned an empty response")
	}
	return domain.AgentResult{Response: text}, nil
}

// args builds the pi CLI arguments for one execution.
func (a *PiAgent) args() []string {
	args := []string{"--mode", "rpc", "--no-session"}
	if a.cfg.Provider != "" {
		args = append(args, "--provider", a.cfg.Provider)
	}
	if a.cfg.Model != "" {
		args = append(args, "--model", a.cfg.Model)
	}
	if a.cfg.NoTools {
		args = append(args, "--no-tools")
	}
	if a.cfg.SystemPrompt != "" {
		args = append(args, "--append-system-prompt", a.cfg.SystemPrompt)
	}
	return args
}

// run drives the RPC protocol on the already-started process: send the prompt,
// consume events until the run settles, request the final assistant text, and
// return it (empty string when the run produced no text).
func (a *PiAgent) run(ctx context.Context, stdin io.Writer, stdout io.Reader, instruction string) (string, error) {
	scanner := bufio.NewScanner(stdout)
	// RPC uses strict LF framing; bufio.Scanner splits on '\n' at byte level,
	// which is JSONL-compliant (unlike Node readline, which also splits on
	// U+2028/U+2029). Allow large lines (e.g. a long assistant response).
	scanner.Buffer(make([]byte, 0, 64*1024), piMaxLine)

	if err := writeJSON(stdin, piPrompt(instruction)); err != nil {
		return "", fmt.Errorf("pi: write prompt: %w", err)
	}

	var st piRunState
	for scanner.Scan() {
		if stop, err := a.handleLine(ctx, stdin, scanner.Bytes(), &st); err != nil {
			return "", err
		} else if stop {
			break
		}
	}

	if err := scanner.Err(); err != nil && !errors.Is(err, io.EOF) {
		if ctx.Err() != nil {
			return "", fmt.Errorf("pi: timed out after %s", a.cfg.Timeout)
		}
		return "", fmt.Errorf("pi: read: %w", err)
	}
	if ctx.Err() != nil {
		return "", fmt.Errorf("pi: timed out after %s", a.cfg.Timeout)
	}

	switch {
	case st.promptRejected != nil:
		return "", fmt.Errorf("%w: pi: %v", domain.ErrActionPermanent, st.promptRejected)
	case st.runnerErr != nil:
		return "", fmt.Errorf("pi: agent failed after retries: %w", st.runnerErr)
	case st.gotTextResponse:
		return st.text, nil
	case st.settled:
		// The run settled but produced no response text (e.g. a transient
		// failure without a retry marker): retry; never deliver an empty
		// notification.
		return "", errors.New("pi: agent settled without a response text")
	default:
		// EOF before agent_settled: the process crashed or was interrupted.
		return "", errors.New("pi: process exited before agent_settled")
	}
}

// piRunState accumulates the protocol facts handleLine extracts from events.
type piRunState struct {
	promptRejected  error
	settled         bool
	gotTextResponse bool
	text            string
	runnerErr       error
}

// handleLine processes one RPC event line. It returns stop=true when the run
// outcome is decided and no further lines are needed. A non-nil error is a
// hard protocol/IO failure (retryable at Execute's boundary), except that
// prompt rejection is recorded in st and classified as permanent there.
func (a *PiAgent) handleLine(ctx context.Context, stdin io.Writer, line []byte, st *piRunState) (bool, error) {
	var ev rpcEvent
	if err := json.Unmarshal(line, &ev); err != nil {
		// Protocol junk: skip and keep waiting. It is never permanent: a single
		// malformed line must not retire a healthy trigger.
		a.logger.Printf("pi: skip malformed line: %v", err)
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
			st.gotTextResponse = true
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
			if data.Text != nil {
				st.text = *data.Text
			}
			return true, nil

		default:
			// Other command responses (set_model, get_state, ...) never occur in
			// this one-shot flow; ignore.
		}

	case "agent_settled":
		// The run is fully settled: no automatic retry, compaction retry, or
		// queued continuation remains. Only now is the final text meaningful.
		st.settled = true
		if st.runnerErr != nil {
			// The provider already failed after Pi's internal retries: no text
			// will come. Stop scanning; the outcome is retryable.
			return true, nil
		}
		if !st.gotTextResponse {
			if err := writeJSON(stdin, piGetLastAssistantText()); err != nil {
				return false, fmt.Errorf("pi: write get_last_assistant_text: %w", err)
			}
		}

	case "auto_retry_end":
		// Pi retried transient provider errors internally. A failed retry ends
		// the run: record it so the adapter returns retryable (never permanent)
		// and skips the final text request.
		if ev.Success != nil && !*ev.Success {
			st.runnerErr = errors.New(nonEmpty(ev.FinalError, "provider error after retries"))
		}

	case "extension_ui_request":
		// Extensions may request UI. Dialog methods block until answered, so a
		// headless runtime must dismiss them; fire-and-forget methods are
		// ignored. With --no-tools the agent cannot act on responses anyway.
		if isDialog(ev.Method) {
			if err := writeJSON(stdin, map[string]any{
				"type": "extension_ui_response",
				"id":   ev.ID,
				// Dismiss: the extension receives undefined/false.
				"cancelled": true,
			}); err != nil {
				return false, fmt.Errorf("pi: dismiss extension UI dialog: %w", err)
			}
		}

	case "extension_error":
		a.logger.Printf("pi: extension error: %v", ev.Error)

	default:
		// message_start/update/end, turn_start/end, agent_start/end,
		// compaction_*, queue_update, ... are not needed to extract the final
		// text; ignore.
	}

	return false, nil
}

// rpcEvent is the minimal envelope shared by every RPC line (commands, events
// and extension UI requests). Only the fields the adapter reads are decoded.
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

// promptCommand/response/data shapes (documented in the pi RPC protocol).
func piPrompt(instruction string) map[string]any {
	return map[string]any{"id": "alter-prompt", "type": "prompt", "message": instruction}
}

func piGetLastAssistantText() map[string]any {
	return map[string]any{"id": "alter-text", "type": "get_last_assistant_text"}
}

func writeJSON(w io.Writer, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(w, "%s\n", b)
	return err
}

// isDialog reports whether an extension UI method blocks for a client response.
func isDialog(method string) bool {
	switch method {
	case "select", "confirm", "input", "editor":
		return true
	default:
		return false
	}
}

func nonEmpty(s, def string) string {
	if s != "" {
		return s
	}
	return def
}

func boundedText(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max]
}

// Timeouts and bounds for the one-shot RPC execution.
const (
	// piMaxLine is the maximum length of a single RPC line (assistant responses
	// can be large).
	piMaxLine = 8 * 1024 * 1024
	// piReapGrace bounds how long Execute waits for the subprocess to exit
	// after stdin closes, before killing it.
	piReapGrace = 5 * time.Second
)
