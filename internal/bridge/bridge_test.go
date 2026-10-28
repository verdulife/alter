package bridge

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// The fake "pi binary" is the test binary itself re-executed as an RPC server
// (see TestFakePiProcess). WithCommandFactory points the Session at it, so every
// test is hermetic, needs no real pi and no real LLM. The fake keeps serving
// prompts on the SAME process, which is what lets tests prove persistence.

const (
	envFakeScenario = "BRIDGE_FAKE_SCENARIO"
	envFakeCwdFile  = "BRIDGE_FAKE_CWD_FILE"
)

// fakeSpawns records the args of every process the Session spawned, in order.
type fakeSpawns struct {
	mu   sync.Mutex
	args [][]string
}

func (f *fakeSpawns) record(args []string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.args = append(f.args, args)
}

func (f *fakeSpawns) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.args)
}

func (f *fakeSpawns) argsAt(i int) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if i < 0 || i >= len(f.args) {
		return nil
	}
	return f.args[i]
}

// fakeFactory builds a command factory serving each spawn with the given
// scenario, in order; the last scenario repeats for any further spawn.
func fakeFactory(spawns *fakeSpawns, cwdFile string, scenarios ...string) func(string, ...string) *exec.Cmd {
	i := 0
	return func(_ string, args ...string) *exec.Cmd {
		scenario := scenarios[len(scenarios)-1]
		if i < len(scenarios) {
			scenario = scenarios[i]
		}
		i++
		cmd := exec.Command(os.Args[0], "-test.run=TestFakePiProcess")
		env := append(os.Environ(), envFakeScenario+"="+scenario)
		if cwdFile != "" {
			env = append(env, envFakeCwdFile+"="+cwdFile)
		}
		cmd.Env = env
		if spawns != nil {
			spawns.record(args)
		}
		return cmd
	}
}

// newTestSession builds a Session wired to the fake pi.
func newTestSession(t *testing.T, cfg Config, spawns *fakeSpawns, cwdFile string, scenarios ...string) *Session {
	t.Helper()
	if cfg.Timeout <= 0 {
		cfg.Timeout = 10 * time.Second
	}
	return NewSession(cfg,
		WithCommandFactory(fakeFactory(spawns, cwdFile, scenarios...)),
		WithLogger(log.New(io.Discard, "", 0)),
	)
}

// TestFakePiProcess is the helper-process entry point: when the fake env is set
// it serves the canned scenario, otherwise it is a no-op test.
func TestFakePiProcess(t *testing.T) {
	scenario := os.Getenv(envFakeScenario)
	if scenario == "" {
		return
	}
	if file := os.Getenv(envFakeCwdFile); file != "" {
		wd, err := os.Getwd()
		if err != nil {
			os.Exit(1)
		}
		if err := os.WriteFile(file, []byte(wd), 0o600); err != nil {
			os.Exit(1)
		}
	}
	runFakePi(scenario)
	os.Exit(0)
}

// runFakePi serves the RPC protocol on stdin/stdout and stays alive across
// prompts (except in the scenarios that deliberately die).
func runFakePi(scenario string) {
	in := bufio.NewScanner(os.Stdin)
	in.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	emit := func(line string) { _, _ = io.WriteString(os.Stdout, line+"\n") }

	var lastPrompt string
	for in.Scan() {
		var cmd struct {
			Type    string `json:"type"`
			ID      string `json:"id"`
			Message string `json:"message"`
		}
		if json.Unmarshal(in.Bytes(), &cmd) != nil {
			continue
		}
		switch cmd.Type {
		case "prompt":
			lastPrompt = cmd.Message
			accepted := `{"type":"response","command":"prompt","success":true,"id":"` + cmd.ID + `"` +
				`,"data":{"disposition":"started"}}`
			switch scenario {
			case "handled":
				emit(`{"type":"response","command":"prompt","success":true,"id":"` + cmd.ID +
					`","data":{"disposition":"handled"}}`)
			case "hang":
				emit(accepted)
				for { // never settles: the per-prompt timeout must kill the process
					time.Sleep(time.Hour)
				}
			case "crash":
				emit(accepted)
				emit(`{"type":"agent_start"}`)
				return // die before agent_settled -> stdout EOF
			default: // "echo" and "stream"
				emit(accepted)
				if scenario == "stream" {
					emit(`{"type":"message_update","assistantMessageEvent":{"type":"text_delta","contentIndex":0,"delta":"Hola "}}`)
					emit(`{"type":"message_update","assistantMessageEvent":{"type":"text_delta","contentIndex":0,"delta":"mundo"}}`)
				}
				emit(`{"type":"agent_settled"}`)
			}
		case "get_last_assistant_text":
			text := lastPrompt
			if scenario == "stream" {
				// Differs from the accumulated draft on purpose: the final text
				// must come from this authoritative response.
				text = "Hola mundo (autoritativo)"
			}
			encoded, err := json.Marshal(text)
			if err != nil {
				return
			}
			emit(`{"type":"response","command":"get_last_assistant_text","success":true,"id":"` + cmd.ID +
				`","data":{"text":` + string(encoded) + `}}`)
		}
	}
}

func TestPromptStreamAccumulatesDeltasAndReturnsAuthoritativeText(t *testing.T) {
	s := newTestSession(t, Config{}, nil, "", "stream")
	defer s.Close()

	var deltas []string
	text, err := s.PromptStream(context.Background(), "hola", func(accumulated string) {
		deltas = append(deltas, accumulated)
	})
	if err != nil {
		t.Fatalf("PromptStream: %v", err)
	}
	want := []string{"Hola ", "Hola mundo"}
	if len(deltas) != len(want) {
		t.Fatalf("deltas = %v, want %v", deltas, want)
	}
	for i := range want {
		if deltas[i] != want[i] {
			t.Errorf("delta[%d] = %q, want %q", i, deltas[i], want[i])
		}
	}
	if text != "Hola mundo (autoritativo)" {
		t.Errorf("final text = %q, want the get_last_assistant_text value", text)
	}
}

func TestArgsUseCleanLoadToolsExtensionsAndPromptFiles(t *testing.T) {
	spawns := &fakeSpawns{}
	s := newTestSession(t, Config{
		Tools:       "read,grep,find,ls",
		Extensions:  []string{"/ext/tools.ts", "/ext/extra.ts"},
		PromptFiles: []string{"/persona.md", "/formato.md"},
		SessionID:   "alter-telegram",
		VaultDir:    t.TempDir(),
	}, spawns, "", "echo")
	if _, err := s.Prompt(context.Background(), "hola"); err != nil {
		t.Fatalf("Prompt: %v", err)
	}
	s.Close()

	args := spawns.argsAt(0)
	joined := strings.Join(args, " ")
	for _, want := range []string{
		"--mode rpc",
		"--no-extensions",
		"--no-skills",
		"--no-prompt-templates",
		"--no-themes",
		"--no-context-files",
		"--no-approve",
		"--tools read,grep,find,ls",
		"-e /ext/tools.ts",
		"-e /ext/extra.ts",
		"--session-id alter-telegram",
		"--append-system-prompt /persona.md",
		"--append-system-prompt /formato.md",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("args missing %q: got %v", want, args)
		}
	}
	if strings.Contains(joined, "--no-session") {
		t.Errorf("session persistence must stay ON, but args contain --no-session: %v", args)
	}
}

func TestProcessWorkingDirectoryIsTheVault(t *testing.T) {
	vault := t.TempDir()
	cwdFile := filepath.Join(t.TempDir(), "cwd.txt")
	s := newTestSession(t, Config{VaultDir: vault}, nil, cwdFile, "echo")
	if _, err := s.Prompt(context.Background(), "hola"); err != nil {
		t.Fatalf("Prompt: %v", err)
	}
	s.Close()

	raw, err := os.ReadFile(cwdFile)
	if err != nil {
		t.Fatalf("fake pi did not report its working directory: %v", err)
	}
	got := strings.TrimSpace(string(raw))
	if resolved, err := filepath.EvalSymlinks(got); err == nil {
		got = resolved
	}
	want := vault
	if resolved, err := filepath.EvalSymlinks(vault); err == nil {
		want = resolved
	}
	if got != want {
		t.Errorf("pi working directory = %q, want the vault root %q", got, want)
	}
}

func TestHandledDispositionDoesNotWaitForSettle(t *testing.T) {
	s := newTestSession(t, Config{Timeout: 10 * time.Second}, nil, "", "handled")
	defer s.Close()

	start := time.Now()
	if _, err := s.Prompt(context.Background(), "/comando"); err != nil {
		t.Fatalf("Prompt with handled disposition: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("waited %s for a handled prompt; it must not wait for agent_settled", elapsed)
	}
}

func TestTimeoutKillsProcessAndNextPromptRestarts(t *testing.T) {
	spawns := &fakeSpawns{}
	s := newTestSession(t, Config{Timeout: 200 * time.Millisecond}, spawns, "", "hang", "echo")
	defer s.Close()

	_, err := s.Prompt(context.Background(), "colgado")
	if err == nil {
		t.Fatal("expected a timeout error for a hanging run")
	}
	if !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("error = %v, want a timeout error", err)
	}

	got, err := s.Prompt(context.Background(), "segundo")
	if err != nil {
		t.Fatalf("Prompt after timeout: %v", err)
	}
	if got != "segundo" {
		t.Errorf("got %q, want %q", got, "segundo")
	}
	if spawns.count() != 2 {
		t.Errorf("expected a restart (2 spawns), got %d", spawns.count())
	}
}

func TestProcessDeathBeforeSettleIsRetriedOnce(t *testing.T) {
	spawns := &fakeSpawns{}
	s := newTestSession(t, Config{}, spawns, "", "crash", "echo")
	defer s.Close()

	got, err := s.Prompt(context.Background(), "tras el crash")
	if err != nil {
		t.Fatalf("Prompt after crash: %v", err)
	}
	if got != "tras el crash" {
		t.Errorf("got %q, want %q", got, "tras el crash")
	}
	if spawns.count() != 2 {
		t.Errorf("expected exactly one retry (2 spawns), got %d", spawns.count())
	}
}

func TestCloseTwiceIsSafe(t *testing.T) {
	s := newTestSession(t, Config{}, nil, "", "echo")
	if _, err := s.Prompt(context.Background(), "hola"); err != nil {
		t.Fatalf("Prompt: %v", err)
	}
	s.Close()
	s.Close()

	never := newTestSession(t, Config{}, nil, "", "echo")
	never.Close()
}

func TestConcurrentPromptsAreSerialized(t *testing.T) {
	spawns := &fakeSpawns{}
	s := newTestSession(t, Config{}, spawns, "", "echo")
	defer s.Close()

	texts := []string{"primero", "segundo", "tercero", "cuarto"}
	got := make([]string, len(texts))
	errs := make([]error, len(texts))
	var wg sync.WaitGroup
	for i, text := range texts {
		wg.Add(1)
		go func(i int, text string) {
			defer wg.Done()
			got[i], errs[i] = s.Prompt(context.Background(), text)
		}(i, text)
	}
	wg.Wait()

	for i, text := range texts {
		if errs[i] != nil {
			t.Fatalf("Prompt(%q): %v", text, errs[i])
		}
		if got[i] != text {
			t.Errorf("Prompt(%q) = %q: prompts interleaved on the process", text, got[i])
		}
	}
	if spawns.count() != 1 {
		t.Errorf("expected one persistent spawn for four prompts, got %d", spawns.count())
	}
}

// TestEmptyToolsFallsBackToReadOnlyAllowlist pins the safety default: leaving
// Config.Tools empty must never hand Pi its default tool set, which includes
// write tools. S7 depends on the process being read-only.
func TestEmptyToolsFallsBackToReadOnlyAllowlist(t *testing.T) {
	spawns := &fakeSpawns{}
	s := newTestSession(t, Config{VaultDir: t.TempDir()}, spawns, "", "echo")
	if _, err := s.Prompt(context.Background(), "hola"); err != nil {
		t.Fatalf("Prompt: %v", err)
	}
	s.Close()

	joined := strings.Join(spawns.argsAt(0), " ")
	if !strings.Contains(joined, "--tools read,grep,find,ls") {
		t.Errorf("empty Tools must fall back to the read-only allowlist, got %v", spawns.argsAt(0))
	}
}
