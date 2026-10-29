package main

import (
	"context"
	"errors"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/verdu/alter/internal/adapter/telegram"
	"github.com/verdu/alter/internal/config"
)

// This file closes phase 1 with two things the per-slice tests cannot show:
//
//   - the start-up guard: a bridge whose Pi is missing a prompt file or the tools
//     extension must refuse to start instead of answering with a Pi that can
//     write nothing;
//   - one integrated smoke run: a single runtime serving a text turn, an inbound
//     document and an ask_options question, in that order, with the same fakes
//     the slice tests already use.

// TestBridgeStartupRejectsMissingAssets proves the guard runs before anything is
// spawned and names the offending path plus the variable that configured it.
func TestBridgeStartupRejectsMissingAssets(t *testing.T) {
	dir := t.TempDir()
	persona := writeSmokeAsset(t, dir, "persona.md", "# persona\n")
	extension := writeSmokeAsset(t, dir, "index.ts", "// extension\n")
	assetDir := filepath.Join(dir, "carpeta")
	if err := os.Mkdir(assetDir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	for _, tc := range []struct {
		name     string
		prompts  []string
		exts     []string
		wantPath string
		wantDesc string
		wantEnv  string
	}{
		{
			name:     "missing prompt file",
			prompts:  []string{persona, filepath.Join(dir, "no-existe.md")},
			exts:     []string{extension},
			wantPath: filepath.Join(dir, "no-existe.md"),
			wantDesc: "prompt file",
			wantEnv:  "ALTER_BRIDGE_PROMPT_FILES",
		},
		{
			name:     "missing extension",
			prompts:  []string{persona},
			exts:     []string{filepath.Join(dir, "no-existe.ts")},
			wantPath: filepath.Join(dir, "no-existe.ts"),
			wantDesc: "extension",
			wantEnv:  "ALTER_BRIDGE_EXTENSIONS",
		},
		{
			name:     "prompt file is a directory",
			prompts:  []string{assetDir},
			exts:     []string{extension},
			wantPath: assetDir,
			wantDesc: "prompt file",
			wantEnv:  "ALTER_BRIDGE_PROMPT_FILES",
		},
		{
			name:     "extension is a directory",
			prompts:  []string{persona},
			exts:     []string{assetDir},
			wantPath: assetDir,
			wantDesc: "extension",
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
				t.Fatal("newBridgeRuntime succeeded with an unreadable asset; a silent bridge with a useless Pi is exactly what the guard exists to prevent")
			}
			msg := err.Error()
			if !strings.Contains(msg, tc.wantPath) {
				t.Errorf("error %q does not name the offending path %q", msg, tc.wantPath)
			}
			if !strings.Contains(msg, tc.wantDesc) {
				t.Errorf("error %q does not say %q is what is wrong", msg, tc.wantDesc)
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

// TestBridgeStartupAcceptsConfiguredAssets proves a complete configuration
// starts, so the guard rejects only what is genuinely missing.
func TestBridgeStartupAcceptsConfiguredAssets(t *testing.T) {
	dir := t.TempDir()
	persona := writeSmokeAsset(t, dir, "persona.md", "# persona\n")
	extension := writeSmokeAsset(t, dir, "index.ts", "// extension\n")

	spawns := &argsRecorder{}
	rt, err := newBridgeRuntime(smokeConfig(dir, []string{persona}, []string{extension}), discardLogger(),
		withBridgeCommandFactory(fakePiFactory(spawns)))
	if err != nil {
		t.Fatalf("newBridgeRuntime: %v, want the configured assets accepted", err)
	}
	closeRuntime(rt)

	// Nothing is spawned at composition time: the guard must not turn start-up
	// into a Pi launch, and Pi keeps starting on the first prompt.
	if args := spawns.first(); args != nil {
		t.Errorf("Pi was spawned at start-up with %v", args)
	}
}

// TestBridgeStartupSkipsAssetCheckWhenDisabled pins the other half of the guard:
// a runtime that is not a bridge must not care about bridge assets.
func TestBridgeStartupSkipsAssetCheckWhenDisabled(t *testing.T) {
	dir := t.TempDir()
	cfg := smokeConfig(dir, []string{filepath.Join(dir, "no-existe.md")}, []string{filepath.Join(dir, "no-existe.ts")})
	cfg.BridgeEnabled = false

	rt, err := newBridgeRuntime(cfg, discardLogger(),
		withBridgeCommandFactory(fakePiFactory(&argsRecorder{})))
	if err != nil {
		t.Fatalf("newBridgeRuntime: %v, want no asset check outside bridge mode", err)
	}
	closeRuntime(rt)
}

// smokeConfig is the bridge configuration the start-up guard tests compose: a
// complete Telegram setup whose assets are whatever the case under test needs.
func smokeConfig(vaultDir string, prompts, exts []string) config.Config {
	return config.Config{
		TelegramToken:     testBotToken,
		TelegramChatID:    testOwnerID,
		TelegramAPIBase:   "http://127.0.0.1:9",
		VaultDir:          vaultDir,
		BridgeEnabled:     true,
		BridgeSessionID:   "smoke-session",
		BridgeTools:       bridgeToolsAllowlist,
		BridgePromptFiles: prompts,
		BridgeExtensions:  exts,
		BridgeTimeout:     20 * time.Second,
		PiBin:             "pi",
	}
}

// bridgeToolsAllowlist is the S7 allowlist the smoke runtime runs with: the
// read-only tools plus the four deterministic ones, never bash/edit/write.
const bridgeToolsAllowlist = "read,grep,find,ls,save_note,append_note,send_file,ask_options"

func writeSmokeAsset(t *testing.T, dir, name, body string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
	return path
}

func discardLogger() *log.Logger { return log.New(io.Discard, "", 0) }

// closeRuntime releases a composed runtime a test never ran.
func closeRuntime(rt *bridgeRuntime) {
	_ = rt.tools.Close()
	rt.sess.Close()
}

// fakeSmokeBot is the media fake Bot API plus the route inline buttons need, so
// one server covers the three inbound paths of the phase. Everything else is
// delegated to the fakes the slice tests already use.
type fakeSmokeBot struct {
	*fakeMediaBot
	ackMu sync.Mutex
	acks  []string
}

// newFakeSmokeBot starts the smoke Bot API: polling, chat actions, drafts, rich
// messages, getFile/download and callback acknowledgements.
func newFakeSmokeBot(t *testing.T) (*fakeSmokeBot, *httptest.Server) {
	t.Helper()
	b := &fakeSmokeBot{fakeMediaBot: &fakeMediaBot{
		fakeBot:    &fakeBot{delivered: make(chan struct{}, 8)},
		serverPath: map[string]string{},
		body:       map[string][]byte{},
		broken:     map[string]bool{},
		downloads:  map[string]int{},
	}}
	srv := httptest.NewServer(http.HandlerFunc(b.route))
	t.Cleanup(srv.Close)
	return b, srv
}

// route serves the callback acknowledgement and delegates the rest to the media
// fake, which in turn delegates the plain Bot API methods to the base fake.
func (b *fakeSmokeBot) route(w http.ResponseWriter, r *http.Request) {
	if strings.HasSuffix(r.URL.Path, "/answerCallbackQuery") {
		var payload struct {
			CallbackQueryID string `json:"callback_query_id"`
		}
		decodeBotJSON(r, &payload)
		b.ackMu.Lock()
		b.acks = append(b.acks, payload.CallbackQueryID)
		b.ackMu.Unlock()
		writeBotJSON(w, map[string]any{"ok": true, "result": true})
		return
	}
	b.fakeMediaBot.route(w, r)
}

func (b *fakeSmokeBot) acknowledgements() []string {
	b.ackMu.Lock()
	defer b.ackMu.Unlock()
	return append([]string(nil), b.acks...)
}

// smokeHarness is one full bridge runtime under the smoke test.
type smokeHarness struct {
	runtime  *bridgeRuntime
	bot      *fakeSmokeBot
	prompts  string
	vaultDir string
	cancel   func()
	finish   chan struct{}
	runErr   error
}

// startSmokeBridge composes and runs the real wiring with the two fakes, the same
// way the slice tests do, so the smoke run exercises production code only.
func startSmokeBridge(t *testing.T, bot *fakeSmokeBot, srv *httptest.Server) *smokeHarness {
	t.Helper()

	dir := t.TempDir()
	prompts := filepath.Join(dir, "prompts.jsonl")
	vaultDir := t.TempDir()
	t.Setenv(envMediaPiPromptReport, prompts)

	cfg := smokeConfig(vaultDir,
		[]string{writeSmokeAsset(t, dir, "persona.md", "# persona\n")},
		[]string{writeSmokeAsset(t, dir, "index.ts", "// extension\n")},
	)
	cfg.TelegramAPIBase = srv.URL

	rt, err := newBridgeRuntime(cfg, discardLogger(), withBridgeCommandFactory(mediaPiFactory()))
	if err != nil {
		t.Fatalf("newBridgeRuntime: %v", err)
	}

	ctx, cancel := context.WithCancel(t.Context())
	h := &smokeHarness{runtime: rt, bot: bot, prompts: prompts, vaultDir: vaultDir, cancel: cancel, finish: make(chan struct{})}
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

// deliveries returns everything the chat received, rich messages and classic
// fallbacks alike, in the order Telegram accepted them.
func (h *smokeHarness) deliveries() []string {
	finals, messages, _, _ := h.bot.snapshot()
	return append(append([]string{}, finals...), messages...)
}

// waitDeliveries blocks until the chat received at least want deliveries, so the
// test never sleeps for a fixed guess.
func (h *smokeHarness) waitDeliveries(t *testing.T, want int) []string {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if got := h.deliveries(); len(got) >= want {
			return got
		}
		time.Sleep(5 * time.Millisecond)
	}
	finals, messages, drafts, typing := h.bot.snapshot()
	t.Fatalf("timed out waiting for %d deliveries; finals=%v classic=%v drafts=%v typing=%v prompts=%v",
		want, finals, messages, drafts, typing, readSmokePrompts(t, h.prompts))
	return nil
}

// readSmokePrompts returns the prompts the fake Pi received, in order.
func readSmokePrompts(t *testing.T, path string) []string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatalf("read prompt record: %v", err)
	}
	return strings.Split(strings.TrimSpace(string(raw)), "\n")
}

// TestBridgeSmokeServesTextFileAndQuestionInOneRuntime is the integrated proof
// that closes phase 1: one bridge process, three inbound paths, in order.
//
//  1. a text turn is answered;
//  2. an inbound document is stored in the vault and answered;
//  3. an ask_options question is posted with buttons, answered by a callback,
//     and the press never becomes a prompt for Pi.
func TestBridgeSmokeServesTextFileAndQuestionInOneRuntime(t *testing.T) {
	const (
		textMessage  = "hola Pi, ¿qué tengo hoy?"
		documentBody = "informe trimestral de ejemplo"
		question     = "¿Guardo el enlace en el vault?"
	)
	bot, srv := newFakeSmokeBot(t)
	bot.addFile("smoke-doc", "documents/file_smoke.pdf", []byte(documentBody))
	h := startSmokeBridge(t, bot, srv)

	// 1. Text turn.
	pushUpdates(bot.fakeBot, telegram.Update{
		ID: 1,
		Message: &telegram.Message{
			MessageID: 100,
			Chat:      telegram.Chat{ID: testOwnerID},
			Text:      textMessage,
		},
	})
	delivered := h.waitDeliveries(t, 1)
	if delivered[0] != mediaPiAnswer {
		t.Fatalf("first delivery = %q, want the Pi answer %q", delivered[0], mediaPiAnswer)
	}
	prompts := readSmokePrompts(t, h.prompts)
	if len(prompts) != 1 || !strings.Contains(prompts[0], textMessage) {
		t.Fatalf("prompts after the text turn = %v, want the text message as the first prompt", prompts)
	}

	// 2. Inbound document turn.
	pushUpdates(bot.fakeBot, telegram.Update{
		ID: 2,
		Message: &telegram.Message{
			MessageID: 101,
			Chat:      telegram.Chat{ID: testOwnerID},
			Caption:   "resume esto en dos líneas",
			Document: &telegram.Document{
				FileID:   "smoke-doc",
				FileName: "informe.pdf",
				MimeType: "application/pdf",
				FileSize: int64(len(documentBody)),
			},
		},
	})
	delivered = h.waitDeliveries(t, 2)
	if delivered[1] != mediaPiAnswer {
		t.Fatalf("second delivery = %q, want the Pi answer %q", delivered[1], mediaPiAnswer)
	}

	stored := inboxEntries(t, h.vaultDir)
	if len(stored) != 1 {
		t.Fatalf("inbox = %v, want exactly the document that arrived", stored)
	}
	if !strings.HasSuffix(stored[0], "-informe.pdf") {
		t.Errorf("stored name = %q, want the original name", stored[0])
	}
	if got, err := os.ReadFile(filepath.Join(h.vaultDir, "inbox", stored[0])); err != nil {
		t.Fatalf("read stored document: %v", err)
	} else if string(got) != documentBody {
		t.Errorf("stored content = %q, want %q", got, documentBody)
	}

	prompts = readSmokePrompts(t, h.prompts)
	if len(prompts) != 2 {
		t.Fatalf("prompts after the document turn = %v, want the text and the media prompt", prompts)
	}
	if rel := filepath.ToSlash(filepath.Join("inbox", stored[0])); !strings.Contains(prompts[1], rel) {
		t.Errorf("media prompt %q does not carry the vault-relative path %q", prompts[1], rel)
	}
	if !strings.Contains(prompts[1], "resume esto en dos líneas") {
		t.Errorf("media prompt %q does not carry the caption", prompts[1])
	}

	// 3. ask_options question answered by a callback.
	responses := make(chan askHTTPResponse, 1)
	go func() {
		responses <- postAskOptions(t.Context(),
			h.runtime.toolsURL+"/tools/ask_options", h.runtime.tools.Token(),
			question, []string{"guardar", "descartar"})
	}()
	delivered = h.waitDeliveries(t, 3)
	questionMarkdown := delivered[2]
	if !strings.Contains(questionMarkdown, question) {
		t.Fatalf("third delivery = %q, want the question %q", questionMarkdown, question)
	}
	datas := askButtonData(t, questionMarkdown)
	if len(datas) != 2 {
		t.Fatalf("buttons = %v, want one per option", datas)
	}

	pushUpdates(bot.fakeBot, callbackUpdate(3, "cb-smoke", datas[1]))

	var resp askHTTPResponse
	select {
	case resp = <-responses:
	case <-time.After(20 * time.Second):
		t.Fatal("ask_options never returned after the button press")
	}
	if resp.Status != http.StatusOK || resp.Answer != "descartar" || resp.TimedOut {
		t.Errorf("ask_options = %+v, want the pressed option descartar", resp)
	}
	if acks := bot.acknowledgements(); len(acks) != 1 || acks[0] != "cb-smoke" {
		t.Errorf("acknowledged = %v, want [cb-smoke] so the client spinner stops", acks)
	}

	// The press is not free text: Pi must still have seen exactly the two turns
	// above, never a third prompt carrying the callback payload.
	finalPrompts := readSmokePrompts(t, h.prompts)
	if len(finalPrompts) != 2 {
		t.Fatalf("prompts = %v, want only the text and the media turn", finalPrompts)
	}
	for _, prompt := range finalPrompts {
		if strings.Contains(prompt, "cb-smoke") || strings.Contains(prompt, "descartar") {
			t.Errorf("prompt %q carries the callback: a button press must never reach Pi", prompt)
		}
	}

	// The channel is still alive after all three paths.
	if err := h.stop(t); !errors.Is(err, context.Canceled) {
		t.Errorf("Run returned %v, want context.Canceled after cancellation", err)
	}
}

func (h *smokeHarness) stop(t *testing.T) error {
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

// inboxEntries lists the names stored under <vault>/inbox.
func inboxEntries(t *testing.T, vaultDir string) []string {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(vaultDir, "inbox"))
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatalf("read inbox: %v", err)
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	return names
}
