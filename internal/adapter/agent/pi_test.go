package agent

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"os"
	"os/exec"
	"slices"
	"testing"
	"time"

	"github.com/verdu/alter/internal/domain"
)

// --- Deterministic Pi RPC tests (no real LLM) ---------------------------------
//
// The "pi binary" is the test binary itself re-executed as a helper process
// that speaks the documented RPC protocol (see TestPiRPCFake and
// runPiRPCHelper). The PiAgent's WithCommandFactory seam points the adapter at
// that fake, so every test is hermetic and fast.

const (
	envPiRPCFake     = "PI_RPC_HELPER"
	envPiRPCScenario = "PI_RPC_SCENARIO"
)

// fakePiFactory returns a command factory that records the args PiAgent would
// pass and re-executes the test binary as the RPC fake for the given scenario.
func fakePiFactory(t *testing.T, scenario string, args *[]string) func(context.Context, string, ...string) *exec.Cmd {
	t.Helper()
	return func(ctx context.Context, _ string, a ...string) *exec.Cmd {
		*args = append(*args, a...)
		cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=TestPiRPCFake")
		cmd.Env = append(os.Environ(),
			envPiRPCFake+"=1",
			envPiRPCScenario+"="+scenario,
		)
		return cmd
	}
}

// newPiHarness builds a PiAgent whose binary is the RPC fake.
func newPiHarness(t *testing.T, scenario string, cfg PiConfig, args *[]string) *PiAgent {
	t.Helper()
	return NewPiAgent(cfg,
		WithCommandFactory(fakePiFactory(t, scenario, args)),
		WithPiLogger(log.New(io.Discard, "", 0)),
	)
}

// TestPiRPCFake is the helper-process entry point: when the fake env is set it
// runs the canned RPC scenario and exits; otherwise it is a no-op test.
func TestPiRPCFake(t *testing.T) {
	if os.Getenv(envPiRPCFake) != "1" {
		return
	}
	if err := runPiRPCHelper(os.Getenv(envPiRPCScenario)); err != nil {
		os.Exit(1)
	}
	os.Exit(0)
}

// runPiRPCHelper implements the canned RPC scenarios used by the tests.
func runPiRPCHelper(scenario string) error {
	out := os.Stdout
	in := bufio.NewScanner(os.Stdin)
	in.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)

	emit := func(line string) {
		_, _ = io.WriteString(out, line+"\n")
	}
	var promptMessage string

	for in.Scan() {
		var cmd struct {
			Type    string `json:"type"`
			ID      string `json:"id"`
			Message string `json:"message"`
		}
		if err := json.Unmarshal(in.Bytes(), &cmd); err != nil {
			continue
		}

		switch cmd.Type {
		case "prompt":
			emit(`{"type":"response","command":"prompt","success":true,"id":"` + cmd.ID + `"}`)
			switch scenario {
			case "ok", "echo", "ui_request", "empty_text":
				promptMessage = cmd.Message
				if scenario == "ui_request" {
					// A confirm dialog: PiAgent must dismiss it with cancelled.
					emit(`{"type":"extension_ui_request","id":"ui-1","method":"confirm","title":"x"}`)
					if !in.Scan() {
						return nil
					}
				}
				emit(`{"type":"message_start","message":{}}`)
				emit(`{"type":"message_update","assistantMessageEvent":{"type":"text_delta","contentIndex":0,"delta":"Hola "}}`)
				emit(`{"type":"message_update","assistantMessageEvent":{"type":"text_delta","contentIndex":0,"delta":"desde pi"}}`)
				emit(`{"type":"message_end","message":{}}`)
				emit(`{"type":"agent_end","willRetry":false}`)
				emit(`{"type":"agent_settled"}`)

			case "crash":
				// Partial events then silent exit: EOF before agent_settled.
				emit(`{"type":"agent_start"}`)
				return nil

			case "provider_failure":
				// Pi's internal retries exhaust on a transient provider error.
				emit(`{"type":"auto_retry_start","attempt":1,"maxAttempts":2,"delayMs":1,"errorMessage":"529 overloaded"}`)
				emit(`{"type":"auto_retry_end","success":false,"attempt":2,"finalError":"529 overloaded_error: Overloaded"}`)
				emit(`{"type":"agent_settled"}`)

			case "prompt_rejected":
				// Rejected before acceptance: a config/contract failure.
				emit(`{"type":"response","command":"prompt","success":false,"error":"Model not found: invalid/model"}`)
				return nil

			case "hang":
				// Never settle: the adapter's timeout must kill it.
				for {
					time.Sleep(time.Hour)
				}
			}

		case "get_last_assistant_text":
			switch scenario {
			case "ok", "ui_request":
				emit(`{"type":"response","command":"get_last_assistant_text","success":true,"data":{"text":"Hola desde pi"}}`)
			case "echo":
				emit(`{"type":"response","command":"get_last_assistant_text","success":true,"data":{"text":"` + promptMessage + `"}}`)
			case "empty_text":
				emit(`{"type":"response","command":"get_last_assistant_text","success":true,"data":{"text":null}}`)
			case "provider_failure":
				emit(`{"type":"response","command":"get_last_assistant_text","success":true,"data":{"text":null}}`)
			}
			return nil
		}
	}
	return nil
}

func TestPiAgentSuccess(t *testing.T) {
	var args []string
	// NoTools=true is the runtime default (config.Load); the adapter honors the
	// explicit value.
	a := newPiHarness(t, "ok", PiConfig{NoTools: true}, &args)

	res, err := a.Execute(context.Background(), domain.AgentRequest{Instruction: "dime algo"})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.Response != "Hola desde pi" {
		t.Errorf("Response = %q, want %q", res.Response, "Hola desde pi")
	}
	assertArgsContain(t, args, "--mode", "rpc")
	assertArgsContain(t, args, "--no-session")
	// Safe default: tools disabled unless explicitly enabled.
	assertArgsContain(t, args, "--no-tools")
	// Provider/model are optional overrides: absent when not configured.
	assertArgsAbsent(t, args, "--provider")
	assertArgsAbsent(t, args, "--model")
}

func TestPiAgentInstructionReachesWire(t *testing.T) {
	var args []string
	a := newPiHarness(t, "echo", PiConfig{}, &args)

	res, err := a.Execute(context.Background(), domain.AgentRequest{Instruction: "la instruccion viaja"})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.Response != "la instruccion viaja" {
		t.Errorf("Response = %q, want the sent instruction", res.Response)
	}
}

func TestPiAgentProviderAndModelFlags(t *testing.T) {
	var args []string
	a := newPiHarness(t, "ok", PiConfig{Provider: "anthropic", Model: "anthropic/claude-sonnet-4:high"}, &args)

	if _, err := a.Execute(context.Background(), domain.AgentRequest{Instruction: "x"}); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	assertArgsContain(t, args, "--provider", "anthropic")
	assertArgsContain(t, args, "--model", "anthropic/claude-sonnet-4:high")
}

func TestPiAgentNoToolsExplicitlyOverridden(t *testing.T) {
	var args []string
	a := newPiHarness(t, "ok", PiConfig{NoTools: false}, &args)

	if _, err := a.Execute(context.Background(), domain.AgentRequest{Instruction: "x"}); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	assertArgsAbsent(t, args, "--no-tools")
}

func TestPiAgentSystemPromptFlag(t *testing.T) {
	var args []string
	a := newPiHarness(t, "ok", PiConfig{SystemPrompt: "Be brief."}, &args)

	if _, err := a.Execute(context.Background(), domain.AgentRequest{Instruction: "x"}); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	assertArgsContain(t, args, "--append-system-prompt", "Be brief.")
}

func TestPiAgentPromptRejectedIsPermanent(t *testing.T) {
	var args []string
	a := newPiHarness(t, "prompt_rejected", PiConfig{}, &args)

	_, err := a.Execute(context.Background(), domain.AgentRequest{Instruction: "x"})
	if err == nil {
		t.Fatal("expected an error on prompt rejection")
	}
	if !errors.Is(err, domain.ErrActionPermanent) {
		t.Errorf("err = %v, want domain.ErrActionPermanent", err)
	}
}

func TestPiAgentBinaryMissingIsPermanent(t *testing.T) {
	// Real factory, root binary path that cannot exist: Start returns
	// exec.ErrNotFound. No process is spawned, no helper needed.
	a := NewPiAgent(PiConfig{Bin: "/nonexistent/alter-test-pi"}, WithPiLogger(log.New(io.Discard, "", 0)))

	_, err := a.Execute(context.Background(), domain.AgentRequest{Instruction: "x"})
	if err == nil {
		t.Fatal("expected an error for a missing binary")
	}
	if !errors.Is(err, domain.ErrActionPermanent) {
		t.Errorf("err = %v, want domain.ErrActionPermanent", err)
	}
}

func TestPiAgentEmptyResponseIsRetryable(t *testing.T) {
	var args []string
	a := newPiHarness(t, "empty_text", PiConfig{}, &args)

	_, err := a.Execute(context.Background(), domain.AgentRequest{Instruction: "x"})
	if err == nil {
		t.Fatal("expected an error on an empty response")
	}
	if errors.Is(err, domain.ErrActionPermanent) {
		t.Errorf("err = %v must be retryable, not permanent", err)
	}
}

func TestPiAgentCrashIsRetryable(t *testing.T) {
	var args []string
	a := newPiHarness(t, "crash", PiConfig{}, &args)

	_, err := a.Execute(context.Background(), domain.AgentRequest{Instruction: "x"})
	if err == nil {
		t.Fatal("expected an error when the process exits before settling")
	}
	if errors.Is(err, domain.ErrActionPermanent) {
		t.Errorf("err = %v must be retryable, not permanent", err)
	}
}

func TestPiAgentProviderFailureIsRetryable(t *testing.T) {
	var args []string
	a := newPiHarness(t, "provider_failure", PiConfig{}, &args)

	_, err := a.Execute(context.Background(), domain.AgentRequest{Instruction: "x"})
	if err == nil {
		t.Fatal("expected an error after provider retries are exhausted")
	}
	if errors.Is(err, domain.ErrActionPermanent) {
		t.Errorf("err = %v must be retryable, not permanent", err)
	}
}

func TestPiAgentTimeoutIsRetryable(t *testing.T) {
	var args []string
	a := newPiHarness(t, "hang", PiConfig{Timeout: 100 * time.Millisecond}, &args)

	start := time.Now()
	_, err := a.Execute(context.Background(), domain.AgentRequest{Instruction: "x"})
	if err == nil {
		t.Fatal("expected a timeout error")
	}
	if errors.Is(err, domain.ErrActionPermanent) {
		t.Errorf("err = %v must be retryable, not permanent", err)
	}
	if elapsed := time.Since(start); elapsed > 10*time.Second {
		t.Errorf("timeout took %v, want a prompt failure", elapsed)
	}
}

func TestPiAgentUIDialogIsDismissed(t *testing.T) {
	var args []string
	a := newPiHarness(t, "ui_request", PiConfig{}, &args)

	res, err := a.Execute(context.Background(), domain.AgentRequest{Instruction: "x"})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.Response != "Hola desde pi" {
		t.Errorf("Response = %q, want the response despite a dismissed dialog", res.Response)
	}
}

func TestPiAgentContextCancelledIsRetryable(t *testing.T) {
	var args []string
	a := newPiHarness(t, "hang", PiConfig{Timeout: 5 * time.Second}, &args)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := a.Execute(ctx, domain.AgentRequest{Instruction: "x"})
	if err == nil {
		t.Fatal("expected an error on a cancelled context")
	}
	if errors.Is(err, domain.ErrActionPermanent) {
		t.Errorf("err = %v must be retryable, not permanent", err)
	}
}

// --- helpers -----------------------------------------------------------------

func assertArgsContain(t *testing.T, args []string, want ...string) {
	t.Helper()
	for i := 0; i+len(want) <= len(args); i++ {
		if slices.Equal(args[i:i+len(want)], want) {
			return
		}
	}
	t.Errorf("args = %v, want subsequence %v", args, want)
}

func assertArgsAbsent(t *testing.T, args []string, flag string) {
	t.Helper()
	if slices.Contains(args, flag) {
		t.Errorf("args = %v, want no %q", args, flag)
	}
}
