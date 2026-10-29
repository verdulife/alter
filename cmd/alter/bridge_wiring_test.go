package main

import (
	"bufio"
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
	Args       string `json:"args"`
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
			Cwd:        wd,
			ToolsURL:   os.Getenv("ALTER_TOOLS_URL"),
			ToolsToken: os.Getenv("ALTER_TOOLS_TOKEN"),
			Args:       strings.Join(os.Args[1:], " "),
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

	for in.Scan() {
		var cmd struct {
			Type string `json:"type"`
			ID   string `json:"id"`
		}
		if json.Unmarshal(in.Bytes(), &cmd) != nil {
			continue
		}
		switch cmd.Type {
		case "prompt":
			if scenario == "handled" {
				// Pi consumed the prompt without starting a run: no text follows.
				emit(`{"type":"response","command":"prompt","success":true,"id":"` + cmd.ID +
					`","data":{"disposition":"handled"}}`)
				continue
			}
			emit(`{"type":"response","command":"prompt","success":true,"id":"` + cmd.ID +
				`","data":{"disposition":"started"}}`)
			if scenario == "stream" {
				emit(`{"type":"message_update","assistantMessageEvent":{"type":"text_delta","contentIndex":0,"delta":"Hola "}}`)
				emit(`{"type":"message_update","assistantMessageEvent":{"type":"text_delta","contentIndex":0,"delta":"desde Pi"}}`)
			}
			emit(`{"type":"agent_settled"}`)
		case "get_last_assistant_text":
			// The empty scenario settles with no text at all: the chat must get
			// a human error line instead of an empty message.
			text := ""
			if scenario == "stream" {
				text = "Hola desde Pi, **con markdown**"
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
	for i, arg := range args {
		if arg == flag && i+1 < len(args) {
			return args[i+1]
		}
	}
	return ""
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
