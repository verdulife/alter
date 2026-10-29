package bridge

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
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
	// envFakeEnvFile makes the fake report the environment it actually
	// received, which is how a test proves Config.Env reaches the child.
	envFakeEnvFile = "BRIDGE_FAKE_ENV_FILE"
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
	if file := os.Getenv(envFakeEnvFile); file != "" {
		report, err := json.Marshal(map[string]string{
			"ALTER_TOOLS_URL":    os.Getenv("ALTER_TOOLS_URL"),
			"ALTER_TOOLS_TOKEN":  os.Getenv("ALTER_TOOLS_TOKEN"),
			envInheritedForChild: os.Getenv(envInheritedForChild),
		})
		if err != nil {
			os.Exit(1)
		}
		if err := os.WriteFile(file, report, 0o600); err != nil {
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
	// dialogReply keeps the extension_ui_response the client wrote back in the
	// dialog scenario, so the final text can report exactly what arrived.
	var dialogReply string
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
		case "extension_ui_response":
			// A dialog was answered: the run can settle and report the record.
			dialogReply = string(in.Bytes())
			emit(`{"type":"agent_settled"}`)
		case "prompt":
			lastPrompt = cmd.Message
			accepted := `{"type":"response","command":"prompt","success":true,"id":"` + cmd.ID + `"` +
				`,"data":{"disposition":"started"}}`
			switch scenario {
			case "dialog":
				// One blocking confirm dialog: the run only settles once the
				// client answers it, exactly like Pi.
				emit(accepted)
				emit(`{"type":"extension_ui_request","id":"ui-1","method":"confirm",` +
					`"title":"¿Ejecutar bash?","message":"rm -rf /tmp/prueba"}`)
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
			switch scenario {
			case "stream":
				// Differs from the accumulated draft on purpose: the final text
				// must come from this authoritative response.
				text = "Hola mundo (autoritativo)"
			case "dialog":
				// The child reports the dialog record it actually received.
				text = dialogReply
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

// TestArgsLoadGlobalConfigWithExplicitAssets pins T11 and T13: Pi starts with the
// same global configuration it loads in a terminal, with no --no-* switch left and
// no --no-approve either (the bridge answers approvals itself now), while the
// bridge assets stay additive through -e and --append-system-prompt.
func TestArgsLoadGlobalConfigWithExplicitAssets(t *testing.T) {
	spawns := &fakeSpawns{}
	s := newTestSession(t, Config{
		Tools:       "read,grep,find,ls",
		Extensions:  []string{"/ext/tools.ts", "/ext/extra.ts"},
		PromptFiles: []string{"/persona.md", "/formato.md"},
		SessionID:   "alter-telegram",
		WorkDir:     t.TempDir(),
	}, spawns, "", "echo")
	if _, err := s.Prompt(context.Background(), "hola"); err != nil {
		t.Fatalf("Prompt: %v", err)
	}
	s.Close()

	args := spawns.argsAt(0)
	if len(args) < 2 || args[0] != "--mode" || args[1] != "rpc" {
		t.Fatalf("args must start with --mode rpc, got %v", args)
	}
	// Exact cardinality: a substring match cannot see a duplicated -e or
	// --append-system-prompt, which would change the loaded set, nor a repeated
	// --tools, which replaces the allowlist instead of extending it.
	for _, tc := range []struct {
		flag string
		want int
	}{
		{"-e", 2},
		{"--append-system-prompt", 2},
		{"--tools", 1},
		{"--session-id", 1},
		// T13: approvals are the bridge's job now, so project-file trust is no
		// longer disabled behind the owner's back.
		{"--no-approve", 0},
		{"--no-session", 0},
		// The global load must stay on: each of these disables a piece of it.
		{"--no-extensions", 0},
		{"--no-skills", 0},
		{"--no-prompt-templates", 0},
		{"--no-themes", 0},
		{"--no-context-files", 0},
	} {
		if got := countFlag(args, tc.flag); got != tc.want {
			t.Errorf("%s appears %d times, want %d: %v", tc.flag, got, tc.want, args)
		}
	}
	for _, tc := range []struct {
		flag string
		want []string
	}{
		{"-e", []string{"/ext/tools.ts", "/ext/extra.ts"}},
		{"--append-system-prompt", []string{"/persona.md", "/formato.md"}},
		{"--tools", []string{"read,grep,find,ls"}},
		{"--session-id", []string{"alter-telegram"}},
	} {
		got := flagValues(args, tc.flag)
		if len(got) != len(tc.want) {
			t.Errorf("%s has values %v, want %v: %v", tc.flag, got, tc.want, args)
			continue
		}
		for i := range tc.want {
			if got[i] != tc.want[i] {
				t.Errorf("%s value[%d] = %q, want %q: %v", tc.flag, i, got[i], tc.want[i], args)
			}
		}
	}
}

// TestProcessWorkingDirectoryIsTheWorkDir pins cwd = Config.WorkDir. Pi works in
// the owner's directory, not in the notes vault: the vault is only data the tools
// server writes through.
func TestProcessWorkingDirectoryIsTheWorkDir(t *testing.T) {
	workDir := t.TempDir()
	cwdFile := filepath.Join(t.TempDir(), "cwd.txt")
	s := newTestSession(t, Config{WorkDir: workDir}, nil, cwdFile, "echo")
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
	want := workDir
	if resolved, err := filepath.EvalSymlinks(workDir); err == nil {
		want = resolved
	}
	if got != want {
		t.Errorf("pi working directory = %q, want the configured work dir %q", got, want)
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

// envInheritedForChild is a variable the parent test exports so the fake can
// prove the inherited environment still reaches the child.
const envInheritedForChild = "BRIDGE_TEST_INHERITED"

// TestConfigEnvReachesChildOnTopOfInheritedEnvironment proves the child sees a
// variable set through Config.Env (the tools URL and token travel that way) and
// still inherits the environment of the bridge process.
func TestConfigEnvReachesChildOnTopOfInheritedEnvironment(t *testing.T) {
	reportFile := filepath.Join(t.TempDir(), "env.json")
	t.Setenv(envFakeEnvFile, reportFile)
	t.Setenv(envInheritedForChild, "from-parent")

	s := newTestSession(t, Config{
		Env: map[string]string{
			"ALTER_TOOLS_URL":   "http://127.0.0.1:4321",
			"ALTER_TOOLS_TOKEN": "secret-token",
		},
	}, nil, "", "echo")
	if _, err := s.Prompt(context.Background(), "hola"); err != nil {
		t.Fatalf("Prompt: %v", err)
	}
	s.Close()

	raw, err := os.ReadFile(reportFile)
	if err != nil {
		t.Fatalf("fake pi did not report its environment: %v", err)
	}
	var got map[string]string
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("decode report: %v", err)
	}

	if got["ALTER_TOOLS_URL"] != "http://127.0.0.1:4321" {
		t.Errorf("ALTER_TOOLS_URL in the child = %q, want the Config.Env value", got["ALTER_TOOLS_URL"])
	}
	if got["ALTER_TOOLS_TOKEN"] != "secret-token" {
		t.Errorf("ALTER_TOOLS_TOKEN in the child = %q, want the Config.Env value", got["ALTER_TOOLS_TOKEN"])
	}
	if got[envInheritedForChild] != "from-parent" {
		t.Errorf("%s in the child = %q, want the inherited value (Config.Env must not replace os.Environ)", envInheritedForChild, got[envInheritedForChild])
	}
}

// TestChildEnvAppendsConfigEnvInSortedOrder pins the deterministic order of the
// environment Config.Env adds, so the child environment does not depend on Go's
// map iteration order.
func TestChildEnvAppendsConfigEnvInSortedOrder(t *testing.T) {
	s := NewSession(Config{Env: map[string]string{
		"ALTER_ZULU": "3",
		"ALTER_ALFA": "1",
		"ALTER_MIKE": "2",
	}})

	env := s.childEnv(nil)
	if len(env) < 3 {
		t.Fatalf("childEnv(nil) returned %d entries, want the inherited environment plus 3", len(env))
	}
	tail := env[len(env)-3:]
	want := []string{"ALTER_ALFA=1", "ALTER_MIKE=2", "ALTER_ZULU=3"}
	for i := range want {
		if tail[i] != want[i] {
			t.Errorf("childEnv(nil)[%d] = %q, want %q (sorted order)", len(env)-3+i, tail[i], want[i])
		}
	}
}

// TestNoConfigEnvLeavesTheInheritedEnvironmentAlone proves the empty case still
// inherits everything: an unset Env must not hand the child an empty environment.
func TestNoConfigEnvLeavesTheInheritedEnvironmentAlone(t *testing.T) {
	s := NewSession(Config{})
	if env := s.childEnv(nil); env != nil {
		t.Errorf("childEnv() = %d entries, want nil so cmd.Env stays inherited", len(env))
	}
}

// countFlag reports how many times flag appears in args, so a test can assert
// exact cardinality instead of mere presence.
func countFlag(args []string, flag string) int {
	n := 0
	for _, arg := range args {
		if arg == flag {
			n++
		}
	}
	return n
}

// flagValues returns the argument following each occurrence of flag, in order.
func flagValues(args []string, flag string) []string {
	var values []string
	for i, arg := range args {
		if arg == flag && i+1 < len(args) {
			values = append(values, args[i+1])
		}
	}
	return values
}

// countOpenFDs reports how many descriptors this process holds, so a test can
// prove a session released its pipes. It skips where /proc is unavailable.
func countOpenFDs(t *testing.T) int {
	t.Helper()
	entries, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		t.Skipf("no /proc/self/fd: %v", err)
	}
	return len(entries)
}

// unreapedChildren lists the children of this process that already exited but
// were never waited for (state Z): each one is a missing cmd.Wait.
func unreapedChildren(t *testing.T) []string {
	t.Helper()
	var zombies []string
	files, _ := filepath.Glob("/proc/self/task/*/children")
	for _, file := range files {
		raw, err := os.ReadFile(file)
		if err != nil {
			continue
		}
		for _, pid := range strings.Fields(string(raw)) {
			stat, err := os.ReadFile("/proc/" + pid + "/stat")
			if err != nil {
				continue
			}
			s := string(stat)
			// The state sits after the parenthesised command name, which may
			// itself contain spaces and parentheses.
			i := strings.LastIndex(s, ")")
			if i < 0 {
				continue
			}
			if fields := strings.Fields(s[i+1:]); len(fields) > 0 && fields[0] == "Z" {
				zombies = append(zombies, pid)
			}
		}
	}
	return zombies
}

func TestCloseTwiceIsSafe(t *testing.T) {
	baseFDs := countOpenFDs(t)
	s := newTestSession(t, Config{}, nil, "", "echo")
	if _, err := s.Prompt(context.Background(), "hola"); err != nil {
		t.Fatalf("Prompt: %v", err)
	}
	s.Close()
	s.Close()

	// Idempotent shutdown must also leave nothing behind: no child waiting to be
	// reaped and no descriptor still held by the dead process's pipes.
	if zombies := unreapedChildren(t); len(zombies) > 0 {
		t.Errorf("unreaped children after Close %v: cmd.Wait must run on every termination path", zombies)
	}
	if after := countOpenFDs(t); after > baseFDs {
		t.Errorf("descriptors grew from %d to %d across start and Close: the pipes must be released", baseFDs, after)
	}

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

// TestEmptyToolsOmitsTheToolsFlag pins T11: an empty Config.Tools means "no
// restriction", so --tools is not passed at all and Pi keeps its own full
// selection. A silent read-only fallback is exactly what T11 removes.
// dialogRecord is the RPC record Pi emits for one blocking dialog request.
func dialogRecord(t *testing.T, id, method string, extra map[string]any) []byte {
	t.Helper()
	record := map[string]any{"type": "extension_ui_request", "id": id, "method": method}
	for key, value := range extra {
		record[key] = value
	}
	raw, err := json.Marshal(record)
	if err != nil {
		t.Fatalf("encode dialog record: %v", err)
	}
	return raw
}

// answerDialog drives one extension_ui_request through the reader with the given
// configuration and returns the record the bridge wrote back to Pi.
func answerDialog(t *testing.T, cfg Config, ctx context.Context, line []byte) (map[string]any, error) {
	t.Helper()
	s := NewSession(cfg)
	defer s.Close()

	out := &bytes.Buffer{}
	settled, err := s.handleLine(ctx, line, &runState{}, out)
	if settled {
		t.Fatal("handleLine reported the run settled for a dialog request")
	}
	if err != nil {
		return nil, err
	}
	if out.Len() == 0 {
		t.Fatal("the bridge wrote nothing back to Pi: a dialog would hang the extension forever")
	}
	var resp map[string]any
	if err := json.Unmarshal(out.Bytes(), &resp); err != nil {
		t.Fatalf("bridge wrote %q, want one JSON record: %v", out.String(), err)
	}
	return resp, nil
}

// TestDialogRequestReachesTheCallbackAndItsAnswerReturnsToPi proves the reader
// hands the whole request to Config.OnDialog and writes the answer back instead
// of dismissing the dialog blindly.
func TestDialogRequestReachesTheCallbackAndItsAnswerReturnsToPi(t *testing.T) {
	var got []DialogRequest
	cfg := Config{OnDialog: func(_ context.Context, req DialogRequest) (DialogResponse, error) {
		got = append(got, req)
		return DialogResponse{Confirmed: true}, nil
	}}

	line := dialogRecord(t, "ui-1", "confirm", map[string]any{
		"title":   "¿Ejecutar bash?",
		"message": "rm -rf /tmp/prueba",
	})
	resp, err := answerDialog(t, cfg, context.Background(), line)
	if err != nil {
		t.Fatalf("handleLine: %v", err)
	}

	want := DialogRequest{ID: "ui-1", Method: "confirm", Title: "¿Ejecutar bash?", Message: "rm -rf /tmp/prueba"}
	if len(got) != 1 {
		t.Fatalf("OnDialog calls = %d, want exactly one", len(got))
	}
	if !reflect.DeepEqual(got[0], want) {
		t.Errorf("OnDialog got %+v, want %+v", got[0], want)
	}

	if resp["type"] != "extension_ui_response" || resp["id"] != "ui-1" {
		t.Errorf("response = %v, want an extension_ui_response for ui-1", resp)
	}
	if resp["confirmed"] != true {
		t.Errorf("response = %v, want confirmed:true", resp)
	}
	if _, cancelled := resp["cancelled"]; cancelled {
		t.Errorf("response = %v, want the real answer, not a cancellation", resp)
	}
}

// TestDialogSelectAnswerReturnsTheChosenValue pins the select shape: the value
// Pi expects instead of a boolean, and the options carried through untouched.
func TestDialogSelectAnswerReturnsTheChosenValue(t *testing.T) {
	var got []DialogRequest
	cfg := Config{OnDialog: func(_ context.Context, req DialogRequest) (DialogResponse, error) {
		got = append(got, req)
		return DialogResponse{Value: "segunda"}, nil
	}}

	line := dialogRecord(t, "ui-2", "select", map[string]any{
		"title":   "¿Cuál?",
		"options": []string{"primera", "segunda"},
	})
	resp, err := answerDialog(t, cfg, context.Background(), line)
	if err != nil {
		t.Fatalf("handleLine: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("OnDialog calls = %d, want exactly one", len(got))
	}
	if !reflect.DeepEqual(got[0].Options, []string{"primera", "segunda"}) {
		t.Errorf("options = %v, want the two Pi offered", got[0].Options)
	}
	if resp["value"] != "segunda" {
		t.Errorf("response = %v, want value \"segunda\"", resp)
	}
	if _, cancelled := resp["cancelled"]; cancelled {
		t.Errorf("response = %v, want the chosen value, not a cancellation", resp)
	}
}

// TestDialogFailsClosed covers every path that must end in cancelled:true. None
// of them may write a value or a confirmation: an unanswered dialog is a denial.
func TestDialogFailsClosed(t *testing.T) {
	errHandler := errors.New("approval: no answer")
	line := dialogRecord(t, "ui-3", "confirm", map[string]any{
		"title":   "¿Ejecutar bash?",
		"message": "rm -rf /tmp/prueba",
	})

	expired, cancel := context.WithCancel(context.Background())
	cancel()

	for _, tc := range []struct {
		name string
		cfg  Config
		ctx  context.Context
	}{
		{
			name: "callback error",
			cfg: Config{OnDialog: func(context.Context, DialogRequest) (DialogResponse, error) {
				return DialogResponse{}, errHandler
			}},
			ctx: context.Background(),
		},
		{
			name: "expired turn",
			// The callback claims approval, but the turn is over: a late yes
			// must never authorise anything.
			cfg: Config{OnDialog: func(context.Context, DialogRequest) (DialogResponse, error) {
				return DialogResponse{Confirmed: true}, nil
			}},
			ctx: expired,
		},
		{
			name: "callback cancels",
			cfg: Config{OnDialog: func(context.Context, DialogRequest) (DialogResponse, error) {
				return DialogResponse{Cancelled: true}, nil
			}},
			ctx: context.Background(),
		},
		{
			name: "no callback configured",
			cfg:  Config{},
			ctx:  context.Background(),
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resp, err := answerDialog(t, tc.cfg, tc.ctx, line)
			if err != nil {
				t.Fatalf("handleLine: %v, want a cancelled answer instead of a hard failure", err)
			}
			if resp["cancelled"] != true {
				t.Errorf("response = %v, want cancelled:true", resp)
			}
			if _, ok := resp["confirmed"]; ok {
				t.Errorf("response = %v, want no confirmed field on a cancellation", resp)
			}
			if _, ok := resp["value"]; ok {
				t.Errorf("response = %v, want no value field on a cancellation", resp)
			}
			if resp["id"] != "ui-3" {
				t.Errorf("response = %v, want the request id echoed", resp)
			}
		})
	}
}

// TestDialogIgnoresFireAndForgetRequests keeps the reader quiet for the methods
// that expect no answer: writing a response would be a protocol error.
func TestDialogIgnoresFireAndForgetRequests(t *testing.T) {
	for _, method := range []string{"notify", "setStatus", "setWidget", "setTitle", "set_editor_text"} {
		t.Run(method, func(t *testing.T) {
			s := NewSession(Config{OnDialog: func(context.Context, DialogRequest) (DialogResponse, error) {
				t.Errorf("OnDialog was called for the fire-and-forget method %q", method)
				return DialogResponse{}, nil
			}})
			defer s.Close()

			out := &bytes.Buffer{}
			line := dialogRecord(t, "ui-4", method, map[string]any{"message": "trabajando"})
			if _, err := s.handleLine(context.Background(), line, &runState{}, out); err != nil {
				t.Fatalf("handleLine: %v", err)
			}
			if out.Len() != 0 {
				t.Errorf("bridge wrote %q for %q, want nothing: fire-and-forget requests take no answer", out.String(), method)
			}
		})
	}
}

// TestDialogAnswerReachesTheChildProcess proves the write-back end to end: the
// fake pi only settles after it receives the response, and it reports the record
// it got, so a silently dropped answer fails the test.
func TestDialogAnswerReachesTheChildProcess(t *testing.T) {
	s := newTestSession(t, Config{OnDialog: func(_ context.Context, req DialogRequest) (DialogResponse, error) {
		if req.ID != "ui-1" || req.Method != "confirm" {
			t.Errorf("OnDialog got %+v, want the confirm request ui-1", req)
		}
		return DialogResponse{Confirmed: true}, nil
	}}, nil, "", "dialog")
	defer s.Close()

	text, err := s.Prompt(context.Background(), "aprueba")
	if err != nil {
		t.Fatalf("Prompt: %v", err)
	}

	var resp map[string]any
	if err := json.Unmarshal([]byte(text), &resp); err != nil {
		t.Fatalf("final text = %q, want the dialog record the child received: %v", text, err)
	}
	if resp["type"] != "extension_ui_response" || resp["id"] != "ui-1" {
		t.Errorf("child received %v, want an extension_ui_response for ui-1", resp)
	}
	if resp["confirmed"] != true {
		t.Errorf("child received %v, want confirmed:true", resp)
	}
}

func TestEmptyToolsOmitsTheToolsFlag(t *testing.T) {
	spawns := &fakeSpawns{}
	s := newTestSession(t, Config{WorkDir: t.TempDir()}, spawns, "", "echo")
	if _, err := s.Prompt(context.Background(), "hola"); err != nil {
		t.Fatalf("Prompt: %v", err)
	}
	s.Close()

	args := spawns.argsAt(0)
	if got := countFlag(args, "--tools"); got != 0 {
		t.Errorf("--tools appears %d times with empty Tools, want 0: %v", got, args)
	}
	// A value without its flag would be worse than the flag: Pi would take it as
	// a prompt or as the value of another option.
	for _, allowlist := range []string{
		"read,grep,find,ls",
		"read,grep,find,ls,save_note,append_note,send_file,ask_options",
	} {
		for _, arg := range args {
			if arg == allowlist {
				t.Errorf("args carry the tool allowlist %q with empty Tools: %v", allowlist, args)
			}
		}
	}
}
