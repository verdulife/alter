package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/verdu/alter/internal/adapter/telegram"
	"github.com/verdu/alter/internal/bridge"
	"github.com/verdu/alter/internal/config"
)

// The whole slice is driven end to end against two fakes:
//
//   - a fake Telegram Bot API (httptest), reached through
//     cfg.TelegramAPIBase, so polling, chat actions, drafts and the final
//     delivery all run through the real HTTP client;
//   - a fake `pi --mode rpc` process: this test binary re-executed with
//     -test.run=TestBridgeFakePiProcess (the technique of
//     internal/bridge/bridge_test.go), so no real pi binary and no LLM are
//     needed.
const (
	testBotToken = "123456:test-token"
	testOwnerID  = int64(4242)

	envFakePiScenario = "ALTER_TEST_FAKE_PI_SCENARIO"
	envFakePiReport   = "ALTER_TEST_FAKE_PI_REPORT"
)

// piReport is what the fake pi writes about itself, so a test can assert the
// working directory and the tools environment the bridge handed it.
type piReport struct {
	Cwd        string `json:"cwd"`
	ToolsURL   string `json:"tools_url"`
	ToolsToken string `json:"tools_token"`
	// PersonaFile is ALTER_PERSONA_FILE as the child saw it: the extension
	// re-states that file as the last system-prompt block, so it must be the same
	// persona this process appended and an absolute path (Pi runs in WorkDir).
	PersonaFile string `json:"persona_file"`
	Args        string `json:"args"`
}

// TestBridgeFakePiProcess is the helper-process entry point. When the scenario
// variable is unset the test is a no-op in the normal run.
func TestBridgeFakePiProcess(t *testing.T) {
	scenario := os.Getenv(envFakePiScenario)
	if scenario == "" {
		return
	}
	if report := os.Getenv(envFakePiReport); report != "" {
		wd, err := os.Getwd()
		if err != nil {
			os.Exit(1)
		}
		raw, err := json.Marshal(piReport{
			Cwd:         wd,
			ToolsURL:    os.Getenv("ALTER_TOOLS_URL"),
			ToolsToken:  os.Getenv("ALTER_TOOLS_TOKEN"),
			PersonaFile: os.Getenv("ALTER_PERSONA_FILE"),
			Args:        strings.Join(os.Args[1:], " "),
		})
		if err != nil {
			os.Exit(1)
		}
		if err := os.WriteFile(report, raw, 0o600); err != nil {
			os.Exit(1)
		}
	}
	runFakePiScenario(scenario)
	os.Exit(0)
}

// runFakePiScenario serves the RPC protocol on stdin/stdout and stays alive
// across prompts, like a real persistent pi session.
func runFakePiScenario(scenario string) {
	in := bufio.NewScanner(os.Stdin)
	in.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	emit := func(line string) { _, _ = io.WriteString(os.Stdout, line+"\n") }

	// dialogReply keeps the extension_ui_response the bridge wrote back in the
	// dialog scenarios, so the final text reports exactly what Pi received.
	var dialogReply string
	for in.Scan() {
		var cmd struct {
			Type string `json:"type"`
			ID   string `json:"id"`
		}
		if json.Unmarshal(in.Bytes(), &cmd) != nil {
			continue
		}
		switch cmd.Type {
		case "extension_ui_response":
			// The dialog was answered: the run settles and reports the record.
			dialogReply = string(in.Bytes())
			emit(`{"type":"agent_settled"}`)
		case "prompt":
			if scenario == "handled" {
				// Pi consumed the prompt without starting a run: no text follows.
				emit(`{"type":"response","command":"prompt","success":true,"id":"` + cmd.ID +
					`","data":{"disposition":"handled"}}`)
				continue
			}
			emit(`{"type":"response","command":"prompt","success":true,"id":"` + cmd.ID +
				`","data":{"disposition":"started"}}`)
			switch scenario {
			case "dialog-confirm", "dialog-select", "dialog-input":
				// One blocking dialog: the run only settles once the bridge answers
				// it, exactly like Pi. The message carries markup characters on
				// purpose, so the question the owner receives proves the escaping.
				emit(dialogRequestLine(scenario))
			case "stream":
				emit(`{"type":"message_update","assistantMessageEvent":{"type":"text_delta","contentIndex":0,"delta":"Hola "}}`)
				emit(`{"type":"message_update","assistantMessageEvent":{"type":"text_delta","contentIndex":0,"delta":"desde Pi"}}`)
				emit(`{"type":"agent_settled"}`)
			default:
				emit(`{"type":"agent_settled"}`)
			}
		case "get_last_assistant_text":
			// The empty scenario settles with no text at all: the chat must get
			// a human error line instead of an empty message.
			text := ""
			switch scenario {
			case "stream":
				text = "Hola desde Pi, **con markdown**"
			default:
				if strings.HasPrefix(scenario, "dialog-") {
					text = dialogReply
				}
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

// dialogRequestLine renders the RPC record the fake Pi emits for the dialog
// method of the scenario under test.
func dialogRequestLine(scenario string) string {
	switch scenario {
	case "dialog-select":
		return `{"type":"extension_ui_request","id":"ui-dialog","method":"select",` +
			`"title":"¿Qué hago con el enlace?","options":["guardar","descartar"]}`
	case "dialog-input":
		return `{"type":"extension_ui_request","id":"ui-dialog","method":"input",` +
			`"title":"Escribe un nombre","placeholder":"nombre"}`
	default:
		// The `confirm` dialog is what the extension asks before a destructive
		// tool: the title names the tool and the message carries its arguments.
		return `{"type":"extension_ui_request","id":"ui-dialog","method":"confirm",` +
			`"title":"¿Ejecutar bash?","message":"{\"command\":\"echo <b>hola</b>&& ls\"}"}`
	}
}

// argsRecorder records the argv of every fake pi spawn.
type argsRecorder struct {
	mu   sync.Mutex
	args [][]string
}

func (a *argsRecorder) record(args []string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.args = append(a.args, args)
}

func (a *argsRecorder) first() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	if len(a.args) == 0 {
		return nil
	}
	return a.args[0]
}

// fakePiFactory spawns this test binary as the fake pi.
func fakePiFactory(spawns *argsRecorder) func(string, ...string) *exec.Cmd {
	return func(_ string, args ...string) *exec.Cmd {
		spawns.record(args)
		return exec.Command(os.Args[0], "-test.run=TestBridgeFakePiProcess")
	}
}

// fakeBot is a scripted Telegram Bot API. Every call is recorded so tests assert
// on the observable protocol instead of internals.
type fakeBot struct {
	mu        sync.Mutex
	batches   [][]telegram.Update
	failPolls int
	pollCalls int

	typing   []string
	drafts   []string
	finals   []string
	messages []string

	delivered chan struct{}
}

func newFakeBot(t *testing.T, failPolls int, updates ...telegram.Update) (*fakeBot, *httptest.Server) {
	t.Helper()
	b := &fakeBot{failPolls: failPolls, delivered: make(chan struct{}, 8)}
	if len(updates) > 0 {
		b.batches = [][]telegram.Update{updates}
	}
	srv := httptest.NewServer(http.HandlerFunc(b.handle))
	t.Cleanup(srv.Close)
	return b, srv
}

func (b *fakeBot) handle(w http.ResponseWriter, r *http.Request) {
	method := r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
	switch method {
	case "getUpdates":
		b.mu.Lock()
		fail := b.pollCalls < b.failPolls
		b.pollCalls++
		var batch []telegram.Update
		if !fail && len(b.batches) > 0 {
			batch = b.batches[0]
			b.batches = b.batches[1:]
		}
		b.mu.Unlock()

		if fail {
			// A transport-level poll failure: the loop must log it and keep
			// polling instead of stopping the channel.
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = io.WriteString(w, `{"ok":false,"error_code":500,"description":"transient failure"}`)
			return
		}
		if len(batch) == 0 {
			// Yield so the poll loop does not spin at full speed.
			time.Sleep(2 * time.Millisecond)
		}
		writeBotJSON(w, map[string]any{"ok": true, "result": batch})

	case "sendChatAction":
		var payload struct {
			Action string `json:"action"`
		}
		decodeBotJSON(r, &payload)
		b.mu.Lock()
		b.typing = append(b.typing, payload.Action)
		b.mu.Unlock()
		writeBotJSON(w, map[string]any{"ok": true, "result": map[string]any{}})

	case "sendRichMessageDraft":
		var payload struct {
			DraftID     int `json:"draft_id"`
			RichMessage struct {
				Markdown string `json:"markdown"`
			} `json:"rich_message"`
		}
		decodeBotJSON(r, &payload)
		b.mu.Lock()
		b.drafts = append(b.drafts, payload.RichMessage.Markdown)
		b.mu.Unlock()
		writeBotJSON(w, map[string]any{"ok": true, "result": map[string]any{}})

	case "sendRichMessage":
		var payload struct {
			ChatID      int64 `json:"chat_id"`
			RichMessage struct {
				Markdown string `json:"markdown"`
			} `json:"rich_message"`
		}
		decodeBotJSON(r, &payload)
		b.mu.Lock()
		b.finals = append(b.finals, payload.RichMessage.Markdown)
		b.mu.Unlock()
		// A real Bot API returns the sent Message; the client decodes it, so the
		// fake must answer with an envelope instead of an empty body.
		writeBotJSON(w, map[string]any{"ok": true, "result": map[string]any{
			"message_id": 1,
			"chat":       map[string]any{"id": payload.ChatID},
		}})
		b.signal()

	case "sendMessage":
		var payload struct {
			Text string `json:"text"`
		}
		decodeBotJSON(r, &payload)
		b.mu.Lock()
		b.messages = append(b.messages, payload.Text)
		b.mu.Unlock()
		b.signal()

	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func (b *fakeBot) signal() {
	select {
	case b.delivered <- struct{}{}:
	default:
	}
}

func writeBotJSON(w http.ResponseWriter, body any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(body)
}

func decodeBotJSON(r *http.Request, dst any) {
	_ = json.NewDecoder(r.Body).Decode(dst)
}

// waitDelivery blocks until the bot received one final delivery.
func (b *fakeBot) waitDelivery(t *testing.T) {
	t.Helper()
	select {
	case <-b.delivered:
	case <-time.After(20 * time.Second):
		t.Fatalf("timed out waiting for the final delivery; %s", b.summary())
	}
}

// summary renders the recorded calls for a failure message.
func (b *fakeBot) summary() string {
	finals, messages, drafts, typing := b.snapshot()
	return "finals=" + strings.Join(finals, "|") +
		" classic=" + strings.Join(messages, "|") +
		" drafts=" + strings.Join(drafts, "|") +
		" typing=" + strings.Join(typing, "|")
}

func (b *fakeBot) snapshot() (finals, messages, drafts, typing []string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]string(nil), b.finals...),
		append([]string(nil), b.messages...),
		append([]string(nil), b.drafts...),
		append([]string(nil), b.typing...)
}

// stageRecorder records the teardown stages the runtime reports.
type stageRecorder struct {
	mu     sync.Mutex
	stages []string
}

func (s *stageRecorder) record(stage string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.stages = append(s.stages, stage)
}

func (s *stageRecorder) list() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.stages...)
}

// bridgeHarness is one running wiring under test.
type bridgeHarness struct {
	runtime  *bridgeRuntime
	bot      *fakeBot
	spawns   *argsRecorder
	stages   *stageRecorder
	report   string
	vaultDir string
	// workDir is the cwd the bridge must hand Pi: the owner's directory, not the
	// vault.
	workDir string
	cancel  context.CancelFunc
	// finish is closed once Run has returned; runErr is written before it, so a
	// reader that observed the close sees the error without a second wait.
	finish chan struct{}
	runErr error
}

// startBridge composes the real wiring (newBridgeRuntime) against the fakes and
// runs it.
func startBridge(t *testing.T, scenario string, failPolls int, updates ...telegram.Update) *bridgeHarness {
	t.Helper()
	return startBridgeWith(t, nil, scenario, failPolls, updates...)
}

// startBridgeWith is startBridge with a hook that adjusts the configuration of
// the case under test before the runtime is composed.
func startBridgeWith(t *testing.T, mutate func(*config.Config), scenario string, failPolls int, updates ...telegram.Update) *bridgeHarness {
	t.Helper()

	bot, srv := newFakeBot(t, failPolls, updates...)
	spawns := &argsRecorder{}
	stages := &stageRecorder{}
	report := filepath.Join(t.TempDir(), "pi-report.json")
	vaultDir := t.TempDir()
	workDir := t.TempDir()

	t.Setenv(envFakePiScenario, scenario)
	t.Setenv(envFakePiReport, report)

	cfg := config.Config{
		TelegramToken:   testBotToken,
		TelegramChatID:  testOwnerID,
		TelegramAPIBase: srv.URL,
		VaultDir:        vaultDir,
		WorkDir:         workDir,
		BridgeSessionID: "test-session",
		BridgeTools:     "read,grep,find,ls,save_note,append_note,send_file,ask_options",
		BridgeTimeout:   20 * time.Second,
		PiBin:           "pi",
	}
	if mutate != nil {
		mutate(&cfg)
	}

	rt, err := newBridgeRuntime(cfg, log.New(io.Discard, "", 0),
		withBridgeCommandFactory(fakePiFactory(spawns)),
		withBridgeTeardownHook(stages.record),
	)
	if err != nil {
		t.Fatalf("newBridgeRuntime: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	h := &bridgeHarness{
		runtime:  rt,
		bot:      bot,
		spawns:   spawns,
		stages:   stages,
		report:   report,
		vaultDir: vaultDir,
		workDir:  workDir,
		cancel:   cancel,
		finish:   make(chan struct{}),
	}
	go func() {
		h.runErr = rt.Run(ctx)
		close(h.finish)
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-h.finish:
		case <-time.After(20 * time.Second):
		}
	})
	return h
}

func (h *bridgeHarness) stop(t *testing.T) error {
	t.Helper()
	h.cancel()
	select {
	case <-h.finish:
		return h.runErr
	case <-time.After(20 * time.Second):
		t.Fatal("Run did not return after cancellation")
		return nil
	}
}

func (h *bridgeHarness) readReport(t *testing.T) piReport {
	t.Helper()
	raw, err := os.ReadFile(h.report)
	if err != nil {
		t.Fatalf("fake pi did not report itself: %v", err)
	}
	var got piReport
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("decode pi report: %v", err)
	}
	return got
}

// TestBridgeTextTurnStreamsDraftAndDeliversFinal is the whole slice for one text
// message: typing action, throttled drafts from PromptStream and exactly one
// final delivery with the authoritative markdown.
func TestBridgeTextTurnStreamsDraftAndDeliversFinal(t *testing.T) {
	h := startBridge(t, "stream", 0, telegram.Update{
		ID: 1,
		Message: &telegram.Message{
			MessageID: 10,
			Chat:      telegram.Chat{ID: testOwnerID},
			Text:      "hola Pi",
		},
	})

	h.bot.waitDelivery(t)
	// Give a wrong double delivery the chance to show up before asserting.
	time.Sleep(100 * time.Millisecond)

	finals, messages, drafts, typing := h.bot.snapshot()
	if len(finals) != 1 || len(messages) != 0 {
		t.Fatalf("final deliveries = %v (classic %v), want exactly one rich message", finals, messages)
	}
	if finals[0] != "Hola desde Pi, **con markdown**" {
		t.Errorf("final markdown = %q, want the authoritative assistant text", finals[0])
	}
	if len(drafts) == 0 {
		t.Error("no draft was published: the streamed text must reach the chat while Pi answers")
	}
	if len(drafts) > 0 && !strings.Contains(drafts[0], "Hola") {
		t.Errorf("draft = %q, want the accumulated assistant text", drafts[0])
	}
	if len(typing) == 0 || typing[0] != "typing" {
		t.Errorf("chat actions = %v, want a typing action before the answer", typing)
	}

	report := h.readReport(t)
	if report.ToolsURL == "" || report.ToolsToken == "" {
		t.Errorf("pi environment = %+v, want ALTER_TOOLS_URL and ALTER_TOOLS_TOKEN set", report)
	}
	if !strings.HasPrefix(report.ToolsURL, "http://127.0.0.1:") {
		t.Errorf("ALTER_TOOLS_URL = %q, want a loopback tools server", report.ToolsURL)
	}
	if !samePath(report.Cwd, h.workDir) {
		t.Errorf("pi working directory = %q, want the configured work dir %q", report.Cwd, h.workDir)
	}
	if samePath(report.Cwd, h.vaultDir) {
		t.Errorf("pi working directory = %q: the vault must no longer be Pi's cwd", report.Cwd)
	}

	// An explicitly configured allowlist must still reach Pi verbatim: narrowing is
	// opt-in, and the bridge never adds or drops a tool on its own (T11).
	args := h.spawns.first()
	if got := flagValue(args, "--tools"); got != "read,grep,find,ls,save_note,append_note,send_file,ask_options" {
		t.Errorf("--tools = %q, want the configured allowlist", got)
	}
	for _, forbidden := range []string{"bash", "edit", "write"} {
		if strings.Contains(strings.Join(args, " "), forbidden) {
			t.Errorf("spawn args contain %q, which Pi must never get: %v", forbidden, args)
		}
	}

	// The tools server the bridge started must answer the contract: the T9a
	// ask_options stub reports "sin respuesta" instead of hanging the turn.
	if err := h.stop(t); !errors.Is(err, context.Canceled) {
		t.Errorf("Run returned %v, want context.Canceled after cancellation", err)
	}
}

// TestBridgeAbsolutizesConfiguredAssets proves the wiring hands Pi absolute asset
// paths even when the configuration is relative — which is exactly how the
// shipped defaults arrive ("alter-persona.md",
// "internal/tools/extension/index.ts"). Pi is spawned with cwd =
// ALTER_BRIDGE_WORKDIR ($HOME), so a relative -e made Pi fail with "Extension
// path does not exist" and a relative --append-system-prompt was appended as that
// literal string: the bridge answered with no tools and no persona, silently.
// ALTER_PERSONA_FILE must also reach the child, absolute, as the first configured
// prompt file, because the extension re-states that file as the last
// system-prompt block and two different personas would contradict each other.
func TestBridgeAbsolutizesConfiguredAssets(t *testing.T) {
	dir := t.TempDir()
	personaAbs := writeSmokeAsset(t, dir, "persona.md", "# persona propia\n")
	contractAbs := writeSmokeAsset(t, dir, "contrato.md", "# contrato\n")
	toolsExtAbs := writeSmokeAsset(t, dir, "index.ts", "// tools extension\n")
	extraExtAbs := writeSmokeAsset(t, dir, "extra.ts", "// extra extension\n")

	// Configured exactly as the environment would: relative to the repository
	// root, never absolute.
	prompts := []string{relativeToCwd(t, personaAbs), relativeToCwd(t, contractAbs)}
	extensions := []string{relativeToCwd(t, toolsExtAbs), relativeToCwd(t, extraExtAbs)}

	h := startBridgeWith(t, func(cfg *config.Config) {
		cfg.BridgePromptFiles = prompts
		cfg.BridgeExtensions = extensions
	}, "stream", 0, telegram.Update{
		ID: 1,
		Message: &telegram.Message{
			MessageID: 21,
			Chat:      telegram.Chat{ID: testOwnerID},
			Text:      "hola Pi",
		},
	})

	// The turn proves the real runtime started with these relative assets and
	// reached Pi; the assertions below then inspect the argv Pi was given.
	h.bot.waitDelivery(t)

	args := h.spawns.first()
	if args == nil {
		t.Fatal("pi was never spawned, so there is no argv to check")
	}

	gotExt := flagValues(args, "-e")
	if len(gotExt) != len(extensions) {
		t.Fatalf("-e values = %v, want one per configured extension (%v): %v", gotExt, extensions, args)
	}
	for i, wantAbs := range []string{toolsExtAbs, extraExtAbs} {
		assertAbsoluteAsset(t, "-e", i, gotExt[i], wantAbs, h.workDir)
	}

	gotPrompts := flagValues(args, "--append-system-prompt")
	if len(gotPrompts) != len(prompts) {
		t.Fatalf("--append-system-prompt values = %v, want one per configured prompt file (%v): %v",
			gotPrompts, prompts, args)
	}
	for i, wantAbs := range []string{personaAbs, contractAbs} {
		assertAbsoluteAsset(t, "--append-system-prompt", i, gotPrompts[i], wantAbs, h.workDir)
	}
	// Order is part of the contract: the first prompt file is the persona.
	if !samePath(gotPrompts[0], personaAbs) {
		t.Errorf("first --append-system-prompt = %q, want the first configured prompt file %q", gotPrompts[0], personaAbs)
	}

	// The extension needs the persona file itself, from Pi's cwd: a relative value
	// would point at $HOME instead of the configured file.
	report := h.readReport(t)
	if report.PersonaFile == "" {
		t.Fatalf("ALTER_PERSONA_FILE did not reach Pi (environment %+v): the extension cannot re-state the persona", report)
	}
	assertAbsoluteAsset(t, "ALTER_PERSONA_FILE", 0, report.PersonaFile, personaAbs, h.workDir)
	if !samePath(report.PersonaFile, gotPrompts[0]) {
		t.Errorf("ALTER_PERSONA_FILE = %q but --append-system-prompt[0] = %q: Pi would see two different personas",
			report.PersonaFile, gotPrompts[0])
	}

	// The values really are the configured files, not a plausible-looking fallback.
	for _, asset := range []struct{ path, marker string }{
		{gotExt[0], "// tools extension"},
		{gotExt[1], "// extra extension"},
		{gotPrompts[0], "# persona propia"},
		{gotPrompts[1], "# contrato"},
		{report.PersonaFile, "# persona propia"},
	} {
		body, err := os.ReadFile(asset.path)
		if err != nil {
			t.Errorf("read %q: %v", asset.path, err)
			continue
		}
		if !strings.Contains(string(body), asset.marker) {
			t.Errorf("asset %q contains %q, want the configured %q", asset.path, string(body), asset.marker)
		}
	}
}

// TestBridgeStartupRejectsMissingRelativeAsset covers the asset guard for a
// relative configured value — the shape production ships, and the one the guard
// stats against this process's working directory rather than Pi's. The
// absolute-path cases (missing file, directory instead of file) already live in
// TestBridgeStartupRejectsMissingAssets in bridge_smoke_test.go and are not
// repeated here; this case pins the relative variant.
func TestBridgeStartupRejectsMissingRelativeAsset(t *testing.T) {
	dir := t.TempDir()
	persona := writeSmokeAsset(t, dir, "persona.md", "# persona\n")
	extension := writeSmokeAsset(t, dir, "index.ts", "// extension\n")
	missingPrompt := relativeToCwd(t, filepath.Join(dir, "no-existe.md"))
	missingExtension := relativeToCwd(t, filepath.Join(dir, "no-existe.ts"))

	for _, tc := range []struct {
		name     string
		prompts  []string
		exts     []string
		wantPath string
		wantEnv  string
	}{
		{
			name:     "relative missing prompt file",
			prompts:  []string{relativeToCwd(t, persona), missingPrompt},
			exts:     []string{relativeToCwd(t, extension)},
			wantPath: missingPrompt,
			wantEnv:  "ALTER_BRIDGE_PROMPT_FILES",
		},
		{
			name:     "relative missing extension",
			prompts:  []string{relativeToCwd(t, persona)},
			exts:     []string{missingExtension},
			wantPath: missingExtension,
			wantEnv:  "ALTER_BRIDGE_EXTENSIONS",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			spawns := &argsRecorder{}
			rt, err := newBridgeRuntime(smokeConfig(dir, tc.prompts, tc.exts), discardLogger(),
				withBridgeCommandFactory(fakePiFactory(spawns)))
			if err == nil {
				if rt != nil {
					closeRuntime(rt)
				}
				t.Fatalf("newBridgeRuntime accepted the missing relative asset %q; the guard must reject what Pi could not load", tc.wantPath)
			}
			msg := err.Error()
			if !strings.Contains(msg, tc.wantPath) {
				t.Errorf("error %q does not name the offending relative path %q", msg, tc.wantPath)
			}
			if !strings.Contains(msg, tc.wantEnv) {
				t.Errorf("error %q does not name %q, so the owner cannot fix it", msg, tc.wantEnv)
			}
			if args := spawns.first(); args != nil {
				t.Errorf("Pi was spawned with %v: the guard must fail before any child process starts", args)
			}
		})
	}
}

// TestBridgeWithoutAllowlistRestrictsNoTools proves T11 at the composition level:
// with no BridgeTools configured (the new default) the bridge passes no --tools at
// all, so Pi runs with its own full selection from the global configuration. Pi
// still starts in the configured work dir, and the vault stays only the store.
func TestBridgeWithoutAllowlistRestrictsNoTools(t *testing.T) {
	h := startBridgeWith(t, func(cfg *config.Config) { cfg.BridgeTools = "" }, "stream", 0, telegram.Update{
		ID: 1,
		Message: &telegram.Message{
			MessageID: 13,
			Chat:      telegram.Chat{ID: testOwnerID},
			Text:      "hola Pi",
		},
	})

	h.bot.waitDelivery(t)

	args := h.spawns.first()
	for _, arg := range args {
		if arg == "--tools" {
			t.Errorf("args contain --tools with no configured allowlist: %v", args)
		}
	}
	// The removed read-only allowlist must not come back as a bare value either.
	for _, arg := range args {
		if arg == "read,grep,find,ls" || arg == "read,grep,find,ls,save_note,append_note,send_file,ask_options" {
			t.Errorf("args carry the tool allowlist %q without being asked: %v", arg, args)
		}
	}

	report := h.readReport(t)
	if !samePath(report.Cwd, h.workDir) {
		t.Errorf("pi working directory = %q, want the work dir %q", report.Cwd, h.workDir)
	}
}

// TestBridgeEmptyPiTurnSendsHumanErrorLine proves a turn that produces no text
// never becomes an empty message: Pi consuming the prompt without starting a run
// and a run that settles with an empty text both answer with a human line.
func TestBridgeEmptyPiTurnSendsHumanErrorLine(t *testing.T) {
	for _, tc := range []struct {
		name     string
		scenario string
		want     string
	}{
		{"handled without a run", "handled", bridgeEmptyReply},
		{"settled without text", "empty", bridgeErrorReply},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := startBridge(t, tc.scenario, 0, telegram.Update{
				ID: 1,
				Message: &telegram.Message{
					MessageID: 11,
					Chat:      telegram.Chat{ID: testOwnerID},
					Text:      "hola Pi",
				},
			})

			h.bot.waitDelivery(t)
			time.Sleep(100 * time.Millisecond)

			finals, messages, _, _ := h.bot.snapshot()
			if len(finals) != 1 || len(messages) != 0 {
				t.Fatalf("final deliveries = %v (classic %v), want exactly one message", finals, messages)
			}
			if finals[0] != tc.want {
				t.Errorf("delivered %q, want the human line %q", finals[0], tc.want)
			}
			if strings.TrimSpace(finals[0]) == "" {
				t.Error("an empty Pi turn must never be delivered as an empty message")
			}
		})
	}
}

// TestBridgePollErrorDoesNotStopTheLoop proves a failed getUpdates is retried:
// the message that arrives after the failure is still answered.
func TestBridgePollErrorDoesNotStopTheLoop(t *testing.T) {
	h := startBridge(t, "stream", 1, telegram.Update{
		ID: 7,
		Message: &telegram.Message{
			MessageID: 12,
			Chat:      telegram.Chat{ID: testOwnerID},
			Text:      "tras el fallo",
		},
	})

	h.bot.waitDelivery(t)
	finals, _, _, _ := h.bot.snapshot()
	if len(finals) != 1 {
		t.Fatalf("final deliveries = %v, want the turn answered after the poll error", finals)
	}

	h.bot.mu.Lock()
	polls := h.bot.pollCalls
	h.bot.mu.Unlock()
	if polls < 2 {
		t.Errorf("getUpdates called %d times, want a retry after the failed poll", polls)
	}
}

// TestBridgeCancellationTearsDownInOrder proves cancellation stops the poller,
// then the tools server, then the Pi session.
func TestBridgeCancellationTearsDownInOrder(t *testing.T) {
	h := startBridge(t, "stream", 0)
	report := h.runtime.toolsURL

	// Let the poll loop start before cancelling.
	time.Sleep(50 * time.Millisecond)

	if err := h.stop(t); !errors.Is(err, context.Canceled) {
		t.Fatalf("Run returned %v, want context.Canceled", err)
	}

	if got, want := h.stages.list(), []string{"poller", "tools", "session"}; !equalStages(got, want) {
		t.Errorf("teardown order = %v, want %v", got, want)
	}
	// The tools server must be gone, not merely idle.
	if _, err := http.Post(report+"/tools/ask_options", "application/json", strings.NewReader(`{}`)); err == nil {
		t.Error("tools server still accepts requests after shutdown")
	}
}

// TestDraftThrottleCapsRequestsPerTurn pins the rate cap: the first delta goes
// out immediately and the following ones inside the interval are dropped.
func TestDraftThrottleCapsRequestsPerTurn(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	throttle := &draftThrottle{min: 500 * time.Millisecond, now: func() time.Time { return now }}

	if !throttle.allow() {
		t.Fatal("the first delta must publish a draft immediately")
	}
	now = now.Add(100 * time.Millisecond)
	if throttle.allow() {
		t.Error("a delta inside the interval must not issue another request")
	}
	now = now.Add(100 * time.Millisecond)
	if throttle.allow() {
		t.Error("a delta inside the interval must not issue another request")
	}
	now = now.Add(400 * time.Millisecond)
	if !throttle.allow() {
		t.Error("a delta after the interval must publish again")
	}
}

// TestFileSenderPicksPhotoOrDocument pins the media choice of send_file.
func TestFileSenderPicksPhotoOrDocument(t *testing.T) {
	for _, tc := range []struct {
		path  string
		image bool
	}{
		{"nota.md", false},
		{"foto.JPG", true},
		{"captura.png", true},
		{"animacion.gif", false},
		{"informe.pdf", false},
	} {
		if got := isImagePath(tc.path); got != tc.image {
			t.Errorf("isImagePath(%q) = %v, want %v", tc.path, got, tc.image)
		}
	}
}

func flagValue(args []string, flag string) string {
	values := flagValues(args, flag)
	if len(values) == 0 {
		return ""
	}
	return values[0]
}

// flagValues returns every value passed with flag, in argv order: -e and
// --append-system-prompt repeat once per asset, so a test must see all of them.
func flagValues(args []string, flag string) []string {
	values := make([]string, 0, 1)
	for i, arg := range args {
		if arg == flag && i+1 < len(args) {
			values = append(values, args[i+1])
		}
	}
	return values
}

// relativeToCwd turns an absolute path into a relative one, resolved against this
// process's working directory: the same base verifyBridgeAssets stats a
// configured asset against, and the way the shipped defaults arrive
// ("alter-persona.md", "internal/tools/extension/index.ts"). The asset itself
// stays outside the worktree.
func relativeToCwd(t *testing.T, path string) string {
	t.Helper()
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	rel, err := filepath.Rel(cwd, path)
	if err != nil {
		t.Fatalf("relative path for %q against %q: %v", path, cwd, err)
	}
	if filepath.IsAbs(rel) {
		t.Fatalf("relative path for %q = %q, want a relative value", path, rel)
	}
	return rel
}

// assertAbsoluteAsset checks one asset path Pi received: it must be absolute,
// must not point inside the child's working directory (the defect resolved the
// configured value there and Pi found nothing) and must be the file that was
// configured.
func assertAbsoluteAsset(t *testing.T, flag string, index int, got, wantAbs, childCwd string) {
	t.Helper()
	if !filepath.IsAbs(got) {
		t.Errorf("%s[%d] = %q, want an absolute path: Pi runs in %q, so a relative asset is not found there",
			flag, index, got, childCwd)
		return
	}
	if strings.HasPrefix(got, childCwd+string(filepath.Separator)) {
		t.Errorf("%s[%d] = %q, want a path outside the child's working directory %q", flag, index, got, childCwd)
	}
	if !samePath(got, wantAbs) {
		t.Errorf("%s[%d] = %q, want the configured file %q (no silent fallback to another path)",
			flag, index, got, wantAbs)
	}
}

func samePath(a, b string) bool {
	if resolved, err := filepath.EvalSymlinks(a); err == nil {
		a = resolved
	}
	if resolved, err := filepath.EvalSymlinks(b); err == nil {
		b = resolved
	}
	return a == b
}

func equalStages(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range want {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

// dialogHarness is one running wiring under test whose log is captured, so a test
// can assert what the owner sees and what the bridge explained in the log.
type dialogHarness struct {
	runtime *bridgeRuntime
	bot     *fakeAskBot
	spawns  *argsRecorder
	logs    *bytes.Buffer
	cancel  context.CancelFunc
	finish  chan struct{}
	runErr  error
}

// startDialogBridge composes and runs the real wiring against the ask fake, so a
// dialog started by Pi travels the whole route: reader → broker → Telegram →
// poller → child. A non-positive timeout keeps the 20 s used by the other slice
// tests; a short one drives the fail-closed path. Options adjust the runtime
// before it starts (the turn seam needs them).
func startDialogBridge(t *testing.T, scenario string, timeout time.Duration, opts ...bridgeRuntimeOption) *dialogHarness {
	t.Helper()

	bot, srv := newFakeAskBot(t)
	spawns := &argsRecorder{}
	logs := &bytes.Buffer{}
	t.Setenv(envFakePiScenario, scenario)
	if timeout <= 0 {
		timeout = 20 * time.Second
	}

	cfg := config.Config{
		TelegramToken:   testBotToken,
		TelegramChatID:  testOwnerID,
		TelegramAPIBase: srv.URL,
		VaultDir:        t.TempDir(),
		BridgeSessionID: "dialog-session",
		BridgeTimeout:   timeout,
		PiBin:           "pi",
	}

	rt, err := newBridgeRuntime(cfg, log.New(logs, "", 0),
		append([]bridgeRuntimeOption{withBridgeCommandFactory(fakePiFactory(spawns))}, opts...)...)
	if err != nil {
		t.Fatalf("newBridgeRuntime: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	h := &dialogHarness{runtime: rt, bot: bot, spawns: spawns, logs: logs, cancel: cancel, finish: make(chan struct{})}
	go func() {
		h.runErr = rt.Run(ctx)
		close(h.finish)
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-h.finish:
		case <-time.After(20 * time.Second):
		}
	})
	return h
}

// rich returns every rich message the chat received, in order.
func (h *dialogHarness) rich() []string {
	finals, _, _, _ := h.bot.snapshot()
	return finals
}

// waitRich blocks until the chat received at least want rich messages.
func (h *dialogHarness) waitRich(t *testing.T, want int) []string {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if got := h.rich(); len(got) >= want {
			return got
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %d rich messages, got %v; log: %s", want, h.rich(), h.logs.String())
	return nil
}

// sendText pushes one owner message, which starts a turn.
func (h *dialogHarness) sendText(updateID, messageID int, text string) {
	pushUpdates(h.bot.fakeBot, telegram.Update{
		ID: updateID,
		Message: &telegram.Message{
			MessageID: messageID,
			Chat:      telegram.Chat{ID: testOwnerID},
			Text:      text,
		},
	})
}

// stop cancels the runtime and returns the error Run finished with.
func (h *dialogHarness) stop(t *testing.T) error {
	t.Helper()
	h.cancel()
	select {
	case <-h.finish:
		return h.runErr
	case <-time.After(20 * time.Second):
		t.Fatal("Run did not return after cancellation")
		return nil
	}
}

// decodeDialogAnswer reads the record the child received back from the reply the
// fake Pi produced: its final text is exactly the extension_ui_response line.
func decodeDialogAnswer(t *testing.T, markdown string) map[string]any {
	t.Helper()
	var resp map[string]any
	if err := json.Unmarshal([]byte(markdown), &resp); err != nil {
		t.Fatalf("reply %q is not the dialog record Pi received: %v", markdown, err)
	}
	return resp
}

// TestBridgeConfirmDialogBecomesApprovalButtons drives the whole approval route:
// Pi asks `confirm` before a destructive tool, the bridge posts the question with
// exactly two buttons and the press is what Pi receives. Both answers are pinned,
// because a rejection that reaches Pi as an approval would defeat the gate.
func TestBridgeConfirmDialogBecomesApprovalButtons(t *testing.T) {
	for _, tc := range []struct {
		name          string
		press         int
		wantConfirmed bool
	}{
		{name: "aprobar", press: 0, wantConfirmed: true},
		{name: "rechazar", press: 1, wantConfirmed: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := startDialogBridge(t, "dialog-confirm", 0)
			h.sendText(1, 1, "borra el log")

			question := h.waitRich(t, 1)[0]
			if !strings.Contains(question, "¿Ejecutar bash?") {
				t.Errorf("question %q does not name the tool", question)
			}
			// The arguments are shown, escaped and truncated: the owner must see
			// what will run, and the model's text must never become markup.
			if !strings.Contains(question, "echo &lt;b&gt;hola&lt;/b&gt;&amp;&amp; ls") {
				t.Errorf("question %q does not carry the escaped arguments", question)
			}
			if strings.Contains(question, "<b>") {
				t.Errorf("question %q carries unescaped markup from the tool arguments", question)
			}
			if !strings.Contains(question, ">Aprobar<") || !strings.Contains(question, ">Rechazar<") {
				t.Errorf("question %q does not carry exactly the two approval buttons", question)
			}

			datas := askButtonData(t, question)
			if len(datas) != 2 {
				t.Fatalf("buttons = %d (%v), want exactly two", len(datas), datas)
			}
			for _, data := range datas {
				if len(data) > askMaxCallbackBytes {
					t.Errorf("payload %q is %d bytes, over Telegram's %d limit", data, len(data), askMaxCallbackBytes)
				}
				for _, secret := range []string{"bash", "Aprobar", "echo"} {
					if strings.Contains(data, secret) {
						t.Errorf("payload %q leaks %q: the button data must stay opaque", data, secret)
					}
				}
			}

			pushUpdates(h.bot.fakeBot, callbackUpdate(2, "cb-dialog", datas[tc.press]))
			reply := h.waitRich(t, 2)[1]
			resp := decodeDialogAnswer(t, reply)
			if resp["type"] != "extension_ui_response" || resp["id"] != "ui-dialog" {
				t.Errorf("Pi received %v, want an extension_ui_response for ui-dialog", resp)
			}
			if resp["confirmed"] != tc.wantConfirmed {
				t.Errorf("Pi received confirmed=%v after pressing %v, want %v", resp["confirmed"], tc.name, tc.wantConfirmed)
			}
			if _, cancelled := resp["cancelled"]; cancelled {
				t.Errorf("Pi received %v, want the owner's answer, not a cancellation", resp)
			}
			if acks := h.bot.acknowledgements(); len(acks) != 1 || acks[0] != "cb-dialog" {
				t.Errorf("acknowledged = %v, want [cb-dialog]", acks)
			}

			// T13 also removes --no-approve: Pi must keep its project-file trust
			// check, and approvals are the bridge's job now.
			args := h.spawns.first()
			if args == nil {
				t.Fatal("pi was never spawned")
			}
			for _, arg := range args {
				if arg == "--no-approve" {
					t.Errorf("args still carry --no-approve: %v", args)
				}
			}

			if err := h.stop(t); !errors.Is(err, context.Canceled) {
				t.Errorf("Run returned %v, want context.Canceled", err)
			}
		})
	}
}

// TestBridgeSelectDialogBecomesAButtonList turns a `select` dialog into one button
// per option and returns the chosen option as the value Pi expects.
func TestBridgeSelectDialogBecomesAButtonList(t *testing.T) {
	h := startDialogBridge(t, "dialog-select", 0)
	h.sendText(1, 1, "mira el enlace")

	question := h.waitRich(t, 1)[0]
	if !strings.Contains(question, "¿Qué hago con el enlace?") {
		t.Errorf("question %q does not carry the dialog title", question)
	}
	datas := askButtonData(t, question)
	if len(datas) != 2 {
		t.Fatalf("buttons = %d (%v), want one per option", len(datas), datas)
	}
	if !strings.Contains(question, ">guardar<") || !strings.Contains(question, ">descartar<") {
		t.Errorf("question %q does not render the option labels", question)
	}

	pushUpdates(h.bot.fakeBot, callbackUpdate(2, "cb-select", datas[1]))
	reply := h.waitRich(t, 2)[1]
	resp := decodeDialogAnswer(t, reply)
	if resp["value"] != "descartar" {
		t.Errorf("Pi received %v, want value descartar", resp)
	}
	if _, ok := resp["confirmed"]; ok {
		t.Errorf("Pi received %v, want a value answer for select", resp)
	}
}

// TestBridgeFreeTextDialogIsCancelled proves input/editor are dismissed with
// cancelled instead of inventing an answer, and that the bridge says why in the
// log. A free-text answer is out of scope for this task, so the only safe answer
// is none.
func TestBridgeFreeTextDialogIsCancelled(t *testing.T) {
	h := startDialogBridge(t, "dialog-input", 0)
	h.sendText(1, 1, "pregunta lo que sea")

	rich := h.waitRich(t, 1)
	reply := rich[len(rich)-1]
	resp := decodeDialogAnswer(t, reply)
	if resp["cancelled"] != true {
		t.Errorf("Pi received %v, want cancelled:true", resp)
	}
	if _, ok := resp["value"]; ok {
		t.Errorf("Pi received %v, want no value: the bridge must not answer for the owner", resp)
	}
	// No question was posted: a free-text prompt would need a text reply the
	// bridge cannot collect.
	if len(rich) != 1 {
		t.Errorf("chat received %v, want only the final reply", rich)
	}
	if logs := h.logs.String(); !strings.Contains(logs, "free text") || !strings.Contains(logs, "ui-dialog") {
		t.Errorf("log %q does not explain the cancelled free-text dialog", logs)
	}
}

// TestBridgeApprovalWithoutAnswerBlocksTheTurn is the fail-closed case at the
// wiring level: nobody presses the buttons, the turn deadline fires, and the tool
// is never approved. The chat gets the human error line instead of a silent yes.
func TestBridgeApprovalWithoutAnswerBlocksTheTurn(t *testing.T) {
	h := startDialogBridge(t, "dialog-confirm", 400*time.Millisecond)
	h.sendText(1, 1, "borra el log")

	question := h.waitRich(t, 1)[0]
	if !strings.Contains(question, "Aprobar") {
		t.Fatalf("question %q was not posted with the approval buttons", question)
	}

	rich := h.waitRich(t, 2)
	if rich[1] != bridgeErrorReply {
		t.Errorf("second delivery = %q, want the human error line %q: an unanswered approval blocks the turn", rich[1], bridgeErrorReply)
	}
	if logs := h.logs.String(); !strings.Contains(logs, "approval") {
		t.Errorf("log %q does not report the unanswered approval", logs)
	}
}

// TestDialogQuestionEscapesAndTruncates pins the rendering of the question text
// itself: markup from the tool arguments is escaped, and a long command is
// clipped so one approval cannot fill the chat — but the cut keeps both ends, so
// the dangerous tail is never the part that disappears.
func TestDialogQuestionEscapesAndTruncates(t *testing.T) {
	r := &bridgeRuntime{logger: discardLogger()}

	long := strings.Repeat("a", 4000) + "<tg-button>x</tg-button>"
	question := r.dialogQuestion(bridge.DialogRequest{
		Method:  "confirm",
		Title:   "¿Ejecutar bash?",
		Message: long,
	})
	if !strings.Contains(question, "¿Ejecutar bash?") {
		t.Errorf("question %q does not carry the title", question)
	}
	if strings.Contains(question, "<tg-button>") {
		t.Errorf("question %q lets tool arguments inject markup", question)
	}
	// Head and tail are both kept, with the elided middle marked.
	if !strings.Contains(question, bridgeDialogEllipsis) {
		t.Errorf("question %q does not mark the clipped middle", question)
	}
	if !strings.HasSuffix(question, "&lt;tg-button&gt;x&lt;/tg-button&gt;") {
		t.Errorf("question %q dropped the end of the command: the tail is the part the owner must see", question)
	}
	if got, limit := len([]rune(question)), bridgeDialogTitleLimit+bridgeDialogMessageLimit+4; got > limit {
		t.Errorf("question is %d runes, want at most %d", got, limit)
	}

	short := r.dialogQuestion(bridge.DialogRequest{Method: "confirm", Title: "¿Ejecutar edit?", Message: "a && b"})
	if !strings.Contains(short, "a &amp;&amp; b") {
		t.Errorf("question %q does not escape the ampersand", short)
	}
	if strings.Contains(short, bridgeDialogEllipsis) {
		t.Errorf("question %q marks a cut although nothing was cut", short)
	}
}

// TestDialogQuestionKeepsTheTailOfALongCommand is the L16 finding: a long command
// whose dangerous part is at the end (`... && rm -rf ~/x`) must not reach the
// owner as a harmless head. The Go clip keeps the last characters, and its split
// matches the one the Pi extension applies before the message arrives here, so the
// narrower bridge cap cannot re-cut the tail the extension preserved.
func TestDialogQuestionKeepsTheTailOfALongCommand(t *testing.T) {
	r := &bridgeRuntime{logger: discardLogger()}

	tail := " && rm -rf ~/x"
	command := `{"command":"` + strings.Repeat("echo relleno && ", 200) + tail + `"}`
	question := r.dialogQuestion(bridge.DialogRequest{
		Method:  "confirm",
		Title:   "¿Ejecutar bash?",
		Message: command,
	})

	if !strings.HasSuffix(question, "rm -rf ~/x"+"\"}") {
		t.Errorf("question %q does not end with the dangerous command tail", question)
	}
	if !strings.Contains(question, bridgeDialogEllipsis) {
		t.Errorf("question %q does not mark the clipped middle", question)
	}

	// The shape of the clip: head, marker, tail. A short text is never cut.
	if got := clipDialogText("corto", bridgeDialogMessageLimit); got != "corto" {
		t.Errorf("clipDialogText(%q) = %q, want it untouched", "corto", got)
	}
	// Both halves are as long as the cap, so the body is twice what fits and the
	// clip has to elide a middle: the head is the cap itself, so the two-thirds
	// share below is still inside it.
	head := strings.Repeat("H", bridgeDialogMessageLimit)
	body := head + strings.Repeat("M", bridgeDialogMessageLimit) + "TAIL!"
	clipped := clipDialogText(body, bridgeDialogMessageLimit)
	if got, want := len([]rune(clipped)), bridgeDialogMessageLimit; got != want {
		t.Errorf("clipped length = %d runes, want exactly %d", got, want)
	}
	wantHead := bridgeDialogMessageLimit * 2 / 3
	wantTail := bridgeDialogMessageLimit - wantHead - len([]rune(bridgeDialogEllipsis))
	if !strings.HasPrefix(clipped, head[:wantHead]) {
		t.Errorf("clipped text %q does not keep the first %d runes of the head", clipped, wantHead)
	}
	if !strings.HasSuffix(clipped, "TAIL!") {
		t.Errorf("clipped text %q does not keep the tail", clipped)
	}
	// The middle is gone: the tail keeps exactly its share of filler, so one more
	// "M" than that could only come from the part the clip elided.
	if !strings.Contains(clipped, bridgeDialogEllipsis) || strings.Contains(clipped, strings.Repeat("M", wantTail+1)) {
		t.Errorf("clipped text %q does not elide the middle", clipped)
	}

	// The extension's own cap is narrower than the bridge's, so a preview it
	// already clipped passes through the bridge unchanged: same split, no re-cut.
	preview := clipDialogText(body, 240)
	if got := clipDialogText(preview, bridgeDialogMessageLimit); got != preview {
		t.Errorf("bridge re-cut the extension preview: %q, want %q", got, preview)
	}
}

// TestDialogQuestionKeepsAMultiLineCommandWhole pins what the owner reads on the
// phone: the command arrives inside a code block, with its line breaks intact,
// and the message cap is wide enough that a typical multi-line script is shown
// whole instead of cut in the middle of a line.
func TestDialogQuestionKeepsAMultiLineCommandWhole(t *testing.T) {
	r := &bridgeRuntime{logger: discardLogger()}

	command := strings.Join([]string{
		"cd ~/alter",
		"export TMPDIR=/tmp",
		"rm -rf ~/dist",
		"mkdir -p ~/dist",
		"gofmt -l cmd internal",
		"go vet ./...",
		"go test ./... -race -count=1",
		"go build -o ~/dist/alter ./cmd/alter",
		"cp ~/dist/alter ~/bin/alter",
		"chmod +x ~/bin/alter",
		"go test ./internal/tools/... -run TestTools -count=1",
		"go test ./internal/bridge/... -run TestSession -count=1",
		"echo listo",
	}, "\n")
	// The wire format the extension sends: reason, separator, command. The command
	message := "Va a borrar archivos." + bridgeDialogSeparator + command
	question := r.dialogQuestion(bridge.DialogRequest{Method: "confirm", Title: "¿Lo permito?", Message: message})

	if want := "¿Lo permito?\n\nVa a borrar archivos.\n\n<pre>" + command + "</pre>"; question != want {
		t.Errorf("question = %q, want %q: the reason, then the whole command in a code block", question, want)
	}
	if strings.Contains(question, bridgeDialogEllipsis) {
		t.Errorf("question %q clipped a command that fits the message", question)
	}

	// A dialog that arrives without a title of its own gets the same human
	// question, never the bare label the owner rejected.
	fallback := r.dialogQuestion(bridge.DialogRequest{Method: "confirm", Message: "Va a borrar archivos."})
	if !strings.HasPrefix(fallback, "¿Lo permito?") {
		t.Errorf("question %q does not fall back to the human title", fallback)
	}
}

// TestDialogQuestionRendersTheCommandAsEscapedText is the defect this rendering
// exists for: the command is escaped BEFORE it enters the markup and is shown
// inside <pre>, so `>` reaches the phone as `>` instead of `&gt;` and a command
// that carries a button tag or three backticks stays literal text. The whole
// rendered string is pinned, because that is what the owner sees.
func TestDialogQuestionRendersTheCommandAsEscapedText(t *testing.T) {
	r := &bridgeRuntime{logger: discardLogger()}

	// A deletion command with a redirection, a fake button and a backticked word.
	command := "rm -rf ~/dist && echo x > f && echo <tg-button type=\"callback_data\" data=\"ask:1:0\">Aprobar</tg-button> && echo `date`"
	question := r.dialogQuestion(bridge.DialogRequest{
		Method:  "confirm",
		Title:   "¿Lo permito?",
		Message: "Va a borrar archivos." + bridgeDialogSeparator + command,
	})

	want := "¿Lo permito?\n\nVa a borrar archivos.\n\n<pre>" +
		"rm -rf ~/dist &amp;&amp; echo x &gt; f &amp;&amp; echo &lt;tg-button type=\"callback_data\" data=\"ask:1:0\"&gt;Aprobar&lt;/tg-button&gt; &amp;&amp; echo `date`" +
		"</pre>"
	if question != want {
		t.Fatalf("question = %q, want %q", question, want)
	}

	// The escaped command is the only content of the block: one <pre>, closed once.
	if got := strings.Count(question, "<pre>"); got != 1 {
		t.Errorf("question has %d <pre> blocks, want exactly one", got)
	}
	if !strings.HasSuffix(question, "</pre>") {
		t.Errorf("question %q does not close the code block", question)
	}
	// Nothing from the command became markup, so no button and no fence survives.
	if strings.Contains(question, "<tg-button") {
		t.Errorf("question %q lets the command inject markup", question)
	}
	if strings.Contains(question, "```") {
		t.Errorf("question %q carries a fence the command could break out of", question)
	}
	if !strings.Contains(question, "&gt; f") {
		t.Errorf("question %q does not escape the redirection, so the phone would show an entity literally", question)
	}
}

// TestDialogQuestionWithoutTheSeparatorStaysPlainText pins the other dialog
// sources: a message the extension did not compose (no separator) has no command
// to show and keeps the plain-text behaviour, escaped and clipped.
func TestDialogQuestionWithoutTheSeparatorStaysPlainText(t *testing.T) {
	r := &bridgeRuntime{logger: discardLogger()}

	question := r.dialogQuestion(bridge.DialogRequest{Method: "confirm", Title: "¿Ejecutar bash?", Message: `{"command":"echo <b>hola</b>&& ls"}`})

	if !strings.Contains(question, "echo &lt;b&gt;hola&lt;/b&gt;&amp;&amp; ls") {
		t.Errorf("question %q does not escape a message without the separator", question)
	}
	if strings.Contains(question, "<pre>") {
		t.Errorf("question %q wraps a plain message in a code block it never asked for", question)
	}
}

// TestDialogQuestionClipsOnlyTheCommandAndKeepsItsTail pins the extreme case on
// the new wire format: the clip applies to the command part and keeps both ends,
// so the dangerous tail survives and the markup around it is never cut in half.
func TestDialogQuestionClipsOnlyTheCommandAndKeepsItsTail(t *testing.T) {
	r := &bridgeRuntime{logger: discardLogger()}

	command := strings.Repeat("echo relleno && ", 200) + "rm -rf ~/x"
	question := r.dialogQuestion(bridge.DialogRequest{
		Method:  "confirm",
		Title:   "¿Lo permito?",
		Message: "Va a borrar archivos." + bridgeDialogSeparator + command,
	})

	// The reason is whole and sits before the block: the clip never touched it.
	if want := "Va a borrar archivos.\n\n<pre>"; !strings.Contains(question, want) {
		t.Errorf("question %q does not keep the whole reason before the code block", question)
	}
	if !strings.HasPrefix(question, "¿Lo permito?\n\n") {
		t.Errorf("question %q does not open with the title", question)
	}
	// Head and tail of the command are kept, with the elided middle marked, and
	// the block still closes after the tail.
	if !strings.HasSuffix(question, "rm -rf ~/x</pre>") {
		t.Errorf("question %q does not end with the dangerous command tail inside the block", question)
	}
	if !strings.Contains(question, bridgeDialogEllipsis) {
		t.Errorf("question %q does not mark the clipped middle", question)
	}
	if strings.Contains(question, "</pre>\n") || strings.Contains(question, "<pre>\n") {
		t.Errorf("question %q does not close the code block at the very end", question)
	}
}

// count reports how many times the fake pi was spawned.
func (a *argsRecorder) count() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return len(a.args)
}

// resetSessionIDFromReply reads the new session id out of the /reset confirmation:
// it is the last token of the reply and must be the configured base id plus a
// timestamp, so the owner can tell which conversation is current and a test can
// check that the id the chat was told is the id the next spawn uses.
func resetSessionIDFromReply(t *testing.T, confirmation, base string) string {
	t.Helper()
	i := strings.LastIndex(confirmation, " ")
	if i < 0 {
		t.Fatalf("confirmation %q carries no session id", confirmation)
	}
	id := strings.TrimSpace(confirmation[i+1:])
	rest, ok := strings.CutPrefix(id, base+"-")
	if !ok {
		t.Fatalf("confirmation %q does not state the configured base id %q plus a suffix (id %q)", confirmation, base, id)
	}
	if _, err := time.Parse("20060102-150405", rest); err != nil {
		t.Fatalf("session id %q is not %q plus a YYYYMMDD-HHMMSS timestamp: %v", id, base, err)
	}
	return id
}

// TestBridgeResetStartsAFreshConversation covers the whole /reset route: the
// command is consumed by the bridge (Pi is never spawned, so it can never become a
// prompt), exactly one confirmation reaches the chat stating the new id, the
// configured base id stays visible in the log, and the next ordinary message
// carries the new session id.
func TestBridgeResetStartsAFreshConversation(t *testing.T) {
	h := startDialogBridge(t, "stream", 0)

	h.sendText(1, 1, "/reset")
	rich := h.waitRich(t, 1)
	// Give a wrong second delivery the chance to show up before asserting.
	time.Sleep(100 * time.Millisecond)
	if got := h.rich(); len(got) != 1 {
		t.Fatalf("deliveries = %v, want exactly one confirmation", got)
	}
	if got := h.spawns.count(); got != 0 {
		t.Fatalf("pi was spawned %d times for /reset: the command must never reach Pi as a prompt", got)
	}

	confirmation := rich[0]
	if !strings.Contains(confirmation, "reiniciada") {
		t.Errorf("confirmation %q is not the Spanish reset line", confirmation)
	}
	id := resetSessionIDFromReply(t, confirmation, "dialog-session")
	if logs := h.logs.String(); !strings.Contains(logs, "dialog-session") || !strings.Contains(logs, id) {
		t.Errorf("log %q does not keep the configured base id and the new session id %q visible", logs, id)
	}

	// The next ordinary message is a prompt for Pi, on the conversation the reset
	// started.
	h.sendText(2, 2, "hola Pi")
	rich = h.waitRich(t, 2)
	if rich[1] != "Hola desde Pi, **con markdown**" {
		t.Errorf("second delivery = %q, want the ordinary Pi answer", rich[1])
	}
	if got := h.spawns.count(); got != 1 {
		t.Fatalf("spawns = %d, want 1: only the ordinary message starts Pi", got)
	}
	if got := flagValue(h.spawns.first(), "--session-id"); got != id {
		t.Errorf("next prompt used --session-id %q, want the id the reset announced (%q)", got, id)
	}
}

// TestBridgeSlashCommandThatIsNotResetStillReachesPi pins the exact match: only
// the bare command is a reset. "/reset algo" is an ordinary message, so it runs a
// turn and leaves the session untouched.
func TestBridgeSlashCommandThatIsNotResetStillReachesPi(t *testing.T) {
	h := startDialogBridge(t, "stream", 0)

	h.sendText(1, 1, "/reset algo")
	rich := h.waitRich(t, 1)
	if rich[0] != "Hola desde Pi, **con markdown**" {
		t.Fatalf("delivery = %q, want the ordinary Pi answer: %q is not the reset command", rich[0], "/reset algo")
	}
	if got := h.spawns.count(); got != 1 {
		t.Fatalf("spawns = %d, want 1: %q must reach Pi as a prompt", got, "/reset algo")
	}
	if got := flagValue(h.spawns.first(), "--session-id"); got != "dialog-session" {
		t.Errorf("--session-id = %q, want the configured id untouched: %q is not a reset", got, "/reset algo")
	}
}

// TestBridgeResetWhileATurnIsInFlightIsRefused pins the documented semantics at
// the chat level: while a turn is in flight the reset is refused with a short
// Spanish line, the turn still settles with the owner's answer, and the child is
// never restarted under it.
func TestBridgeResetWhileATurnIsInFlightIsRefused(t *testing.T) {
	h := startDialogBridge(t, "dialog-confirm", 120*time.Second)
	h.sendText(1, 1, "borra el log")

	question := h.waitRich(t, 1)[0]
	if !strings.Contains(question, "Aprobar") {
		t.Fatalf("question = %q, want the approval buttons: the turn must be parked", question)
	}

	h.sendText(2, 2, "/reset")
	rich := h.waitRich(t, 2)
	if rich[1] != bridgeResetBusyReply {
		t.Fatalf("delivery = %q, want the busy line %q", rich[1], bridgeResetBusyReply)
	}
	if got := h.spawns.count(); got != 1 {
		t.Fatalf("spawns = %d, want 1: a refused reset must not restart Pi", got)
	}

	// The refused reset changed nothing: the parked turn still settles.
	pushUpdates(h.bot.fakeBot, callbackUpdate(3, "cb-1", askButtonData(t, question)[0]))
	rich = h.waitRich(t, 3)
	if !strings.HasPrefix(rich[2], `{"confirmed":true`) {
		t.Errorf("delivery = %q, want the answer the parked turn received", rich[2])
	}

	// And once the turn is over the same command works, so the refusal is not a
	// dead end.
	h.sendText(4, 4, "/reset")
	rich = h.waitRich(t, 4)
	resetSessionIDFromReply(t, rich[3], "dialog-session")
}

// TestBridgeResetFailureAnswersTheErrorLine proves a failed reset is not silent:
// the chat gets a short Spanish error and the session stays usable, because a
// reset that failed must leave the conversation exactly as it was.
func TestBridgeResetFailureAnswersTheErrorLine(t *testing.T) {
	h := startDialogBridge(t, "stream", 0, withSessionReset(func(context.Context, string) error {
		return errors.New("boom")
	}))

	h.sendText(1, 1, "/reset")
	rich := h.waitRich(t, 1)
	if rich[0] != bridgeResetFailedReply {
		t.Fatalf("delivery = %q, want the error line %q", rich[0], bridgeResetFailedReply)
	}
	if logs := h.logs.String(); !strings.Contains(logs, "boom") {
		t.Errorf("log %q does not report why the reset failed", logs)
	}

	// Still usable: the next message is an ordinary turn.
	h.sendText(2, 2, "hola Pi")
	rich = h.waitRich(t, 2)
	if rich[1] != "Hola desde Pi, **con markdown**" {
		t.Errorf("delivery after the failed reset = %q, want the ordinary Pi answer", rich[1])
	}
}
