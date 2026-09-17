package bridge

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/verdu/alter/internal/domain"
)

// --- Deterministic RPC tests (no real LLM) -----------------------------------
//
// The "pi binary" is the test binary itself re-executed as a helper process
// that speaks the RPC protocol (see TestBridgeRPCFake / runBridgeRPCFake). The
// WithCommandFactory seam points the Session at that fake, so every test is
// hermetic and fast. Unlike the one-shot adapter, the fake stays alive across
// prompts on the SAME process, which is what lets tests verify persistence.

const (
	envBridgeRPCFake     = "BRIDGE_RPC_HELPER"
	envBridgeRPCScenario = "BRIDGE_RPC_SCENARIO"
)

// fakeFactory returns a command factory that records the args passed to pi and
// re-executes the test binary as the RPC fake for the given scenario.
func fakeFactory(t *testing.T, scenario string, args *[]string) func(string, ...string) *exec.Cmd {
	t.Helper()
	return func(_ string, a ...string) *exec.Cmd {
		if args != nil {
			*args = append(*args, a...)
		}
		cmd := exec.Command(os.Args[0], "-test.run=TestBridgeRPCFake")
		cmd.Env = append(os.Environ(),
			envBridgeRPCFake+"=1",
			envBridgeRPCScenario+"="+scenario,
		)
		return cmd
	}
}

// fakeFactorySeq returns a factory that serves each spawn with the given
// scenario, in order (used to test restart: crash then ok). The last scenario
// repeats for any further spawns.
func fakeFactorySeq(scenarios ...string) func(string, ...string) *exec.Cmd {
	i := 0
	return func(_ string, _ ...string) *exec.Cmd {
		sc := scenarios[i]
		if i < len(scenarios)-1 {
			i++
		}
		cmd := exec.Command(os.Args[0], "-test.run=TestBridgeRPCFake")
		cmd.Env = append(os.Environ(),
			envBridgeRPCFake+"=1",
			envBridgeRPCScenario+"="+sc,
		)
		return cmd
	}
}

// newHarness builds a Session whose binary is the RPC fake for one scenario.
func newHarness(t *testing.T, scenario string, cfg Config, args *[]string) *Session {
	t.Helper()
	if cfg.Timeout <= 0 {
		cfg.Timeout = 5 * time.Second
	}
	return NewSession(cfg,
		WithCommandFactory(fakeFactory(t, scenario, args)),
		WithLogger(log.New(io.Discard, "", 0)),
	)
}

// TestBridgeRPCFake is the helper-process entry point: when the fake env is set
// it runs the canned RPC scenario and keeps looping across prompts; otherwise
// it is a no-op test.
func TestBridgeRPCFake(t *testing.T) {
	if os.Getenv(envBridgeRPCFake) != "1" {
		return
	}
	if err := runBridgeRPCFake(os.Getenv(envBridgeRPCScenario)); err != nil {
		os.Exit(1)
	}
	os.Exit(0)
}

// runBridgeRPCFake implements the canned RPC scenarios used by the tests. It
// intentionally does NOT exit after get_last_assistant_text: it keeps scanning
// stdin so the same process can serve several prompts (persistence).
func runBridgeRPCFake(scenario string) error {
	out := os.Stdout
	in := bufio.NewScanner(os.Stdin)
	in.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)

	emit := func(line string) { _, _ = io.WriteString(out, line+"\n") }
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
			promptMessage = cmd.Message
			switch scenario {
			case "crash":
				// Partial events then exit: EOF before agent_settled.
				emit(`{"type":"agent_start"}`)
				return nil
			case "hang":
				// Never settle: the per-prompt timeout must kill the process.
				for {
					time.Sleep(time.Hour)
				}
			case "prompt_rejected":
				emit(`{"type":"response","command":"prompt","success":false,"error":"Model not found: invalid/model"}`)
				return nil
			case "stream":
				// Streamed assistant text: text_delta events carry the partial text.
				emit(`{"type":"message_start","message":{}}`)
				emit(`{"type":"message_update","assistantMessageEvent":{"type":"text_delta","contentIndex":0,"delta":"Hola "}}`)
				emit(`{"type":"message_update","assistantMessageEvent":{"type":"text_delta","contentIndex":0,"delta":"mundo"}}`)
				emit(`{"type":"message_end","message":{}}`)
				emit(`{"type":"agent_settled"}`)
			default: // echo, empty_text
				emit(`{"type":"message_start","message":{}}`)
				emit(`{"type":"message_end","message":{}}`)
				emit(`{"type":"agent_settled"}`)
			}

		case "get_last_assistant_text":
			switch scenario {
			case "echo":
				text, err := json.Marshal(promptMessage)
				if err != nil {
					return err
				}
				emit(`{"type":"response","command":"get_last_assistant_text","success":true,"data":{"text":` + string(text) + `}}`)
			case "stream":
				emit(`{"type":"response","command":"get_last_assistant_text","success":true,"data":{"text":"Hola mundo"}}`)
			case "empty_text":
				emit(`{"type":"response","command":"get_last_assistant_text","success":true,"data":{"text":null}}`)
			}
			// keep the process alive for the next prompt
		}
	}
	return nil
}

func TestSessionArgsCleanAndPersistent(t *testing.T) {
	var args []string
	s := newHarness(t, "echo", Config{SessionName: "alter-bridge"}, &args)

	ctx := context.Background()
	if _, err := s.Prompt(ctx, "hola"); err != nil {
		t.Fatalf("Prompt: %v", err)
	}
	s.Close()

	joined := strings.Join(args, " ")
	for _, want := range []string{
		"--mode rpc",
		"--name alter-bridge",
		"--no-tools",
		"--no-extensions",
		"--no-skills",
		"--no-prompt-templates",
		"--no-themes",
		"--no-context-files",
		"--no-approve",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("args missing %q: got %v", want, args)
		}
	}
	if strings.Contains(joined, "--no-session") {
		t.Errorf("bridge must keep session persistence, but args contain --no-session: %v", args)
	}
}

func TestSessionPromptsReturnEcho(t *testing.T) {
	// Single prompt returns the assistant text.
	s := newHarness(t, "echo", Config{}, nil)
	got, err := s.Prompt(context.Background(), "hola desde telegram")
	if err != nil {
		t.Fatalf("Prompt: %v", err)
	}
	if got != "hola desde telegram" {
		t.Errorf("got %q, want %q", got, "hola desde telegram")
	}
	s.Close()
}

func TestSessionPersistsAcrossPrompts(t *testing.T) {
	// Two prompts on the SAME process (one spawn): each returns its own echo,
	// proving the process is not respawned per message.
	called := 0
	s := NewSession(Config{}, WithCommandFactory(func(_ string, _ ...string) *exec.Cmd {
		called++
		cmd := exec.Command(os.Args[0], "-test.run=TestBridgeRPCFake")
		cmd.Env = append(os.Environ(), envBridgeRPCFake+"=1", envBridgeRPCScenario+"=echo")
		return cmd
	}), WithLogger(log.New(io.Discard, "", 0)))

	for _, msg := range []string{"buenos dias", "recuerda lo anterior", "tercer mensaje"} {
		got, err := s.Prompt(context.Background(), msg)
		if err != nil {
			t.Fatalf("Prompt(%q): %v", msg, err)
		}
		if got != msg {
			t.Errorf("Prompt(%q) got %q", msg, got)
		}
	}
	s.Close()
	if called != 1 {
		t.Errorf("expected 1 pi spawn (persistent), got %d", called)
	}
}

func TestSessionRestartAfterCrash(t *testing.T) {
	// First process dies mid-run (crash scenario); the Session restarts once
	// and retries, returning the echoed text.
	s := NewSession(Config{}, WithCommandFactory(fakeFactorySeq("crash", "echo")),
		WithLogger(log.New(io.Discard, "", 0)))

	got, err := s.Prompt(context.Background(), "tras el crash")
	if err != nil {
		t.Fatalf("Prompt after crash: %v", err)
	}
	if got != "tras el crash" {
		t.Errorf("got %q, want %q", got, "tras el crash")
	}
	s.Close()
}

func TestSessionStreamsDeltas(t *testing.T) {
	// text_delta events are accumulated and pushed to the onDelta callback; the
	// returned text is the authoritative get_last_assistant_text value.
	s := newHarness(t, "stream", Config{}, nil)
	var got []string
	text, err := s.PromptStream(context.Background(), "x", func(partial string) {
		got = append(got, partial)
	})
	if err != nil {
		t.Fatalf("PromptStream: %v", err)
	}
	if text != "Hola mundo" {
		t.Errorf("final text = %q, want %q", text, "Hola mundo")
	}
	want := []string{"Hola ", "Hola mundo"}
	if len(got) != len(want) {
		t.Fatalf("deltas = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("delta[%d] = %q, want %q", i, got[i], want[i])
		}
	}
	s.Close()
}

func TestSessionEmptyResponse(t *testing.T) {
	s := newHarness(t, "empty_text", Config{}, nil)
	_, err := s.Prompt(context.Background(), "x")
	if err == nil {
		t.Fatal("expected error for empty response")
	}
	if domain.AgentErrorKindOf(err) != domain.AgentErrorKindEmptyResponse {
		t.Errorf("got kind %v, want EmptyResponse", domain.AgentErrorKindOf(err))
	}
	s.Close()
}

func TestSessionPromptRejectedIsPermanent(t *testing.T) {
	s := newHarness(t, "prompt_rejected", Config{}, nil)
	_, err := s.Prompt(context.Background(), "x")
	if err == nil {
		t.Fatal("expected error for rejected prompt")
	}
	if domain.AgentErrorKindOf(err) != domain.AgentErrorKindProvider {
		t.Errorf("got kind %v, want Provider", domain.AgentErrorKindOf(err))
	}
	if !errors.Is(err, domain.ErrActionPermanent) {
		t.Error("rejected prompt must be wrapped with ErrActionPermanent")
	}
	s.Close()
}

func TestSessionHangTimesOut(t *testing.T) {
	s := newHarness(t, "hang", Config{Timeout: 50 * time.Millisecond}, nil)
	_, err := s.Prompt(context.Background(), "x")
	if err == nil {
		t.Fatal("expected timeout error for hang")
	}
	if domain.AgentErrorKindOf(err) != domain.AgentErrorKindTimeout {
		t.Errorf("got kind %v, want Timeout", domain.AgentErrorKindOf(err))
	}
	// After the timeout kill the session must still be usable; a fresh prompt
	// restarts the process (echo now).
	s.Close()
}