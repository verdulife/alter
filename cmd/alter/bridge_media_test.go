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

// The inbound-file slice is driven end to end with the same two-fakes approach
// as the text slice (bridge_wiring_test.go), extended in two directions:
//
//   - a fake Bot API that also serves getFile and the file download endpoint, so
//     the real client streams the attachment into the real vault store;
//   - a fake `pi --mode rpc` that records every prompt, so a test can assert
//     what Pi was actually told about the stored file.
const (
	// envMediaPiPromptReport points the fake Pi at the file where it appends the
	// prompts it receives.
	envMediaPiPromptReport = "ALTER_TEST_MEDIA_PI_PROMPT_REPORT"

	// mediaPiAnswer is the fixed line the fake Pi answers with, so a test can
	// tell "the turn ran" from "the chat got a human error line".
	mediaPiAnswer = "He recibido el archivo."

	// testInboundCap is the inbound attachment ceiling the media wiring runs with
	// in these tests, small enough to exercise both size refusals with tiny
	// payloads.
	testInboundCap int64 = 1024
)

// TestBridgeMediaFakePiProcess is the helper-process entry point of the media
// slice. Without the report variable it is a no-op in the normal run.
func TestBridgeMediaFakePiProcess(t *testing.T) {
	report := os.Getenv(envMediaPiPromptReport)
	if report == "" {
		return
	}
	in := bufio.NewScanner(os.Stdin)
	in.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	emit := func(line string) { _, _ = io.WriteString(os.Stdout, line+"\n") }

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
			// Recorded before the answer, so a test that saw the delivery also
			// sees the prompt that produced it.
			if err := appendPrompt(report, cmd.Message); err != nil {
				os.Exit(1)
			}
			emit(`{"type":"response","command":"prompt","success":true,"id":"` + cmd.ID +
				`","data":{"disposition":"started"}}`)
			emit(`{"type":"agent_settled"}`)
		case "get_last_assistant_text":
			encoded, err := json.Marshal(mediaPiAnswer)
			if err != nil {
				os.Exit(1)
			}
			emit(`{"type":"response","command":"get_last_assistant_text","success":true,"id":"` + cmd.ID +
				`","data":{"text":` + string(encoded) + `}}`)
		}
	}
	os.Exit(0)
}

// appendPrompt writes one JSON line per prompt the fake Pi received.
func appendPrompt(path, prompt string) error {
	encoded, err := json.Marshal(prompt)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.Write(append(encoded, '\n'))
	return err
}

// mediaPiFactory spawns this test binary as the prompt-recording fake Pi.
func mediaPiFactory() func(string, ...string) *exec.Cmd {
	return func(string, ...string) *exec.Cmd {
		return exec.Command(os.Args[0], "-test.run=TestBridgeMediaFakePiProcess")
	}
}

// fakeMediaBot is the text-slice fake Bot API plus the two routes an inbound
// file needs: getFile and the file download endpoint. Every other method is
// delegated to the base fake, so updates, typing, drafts and final deliveries
// are recorded exactly as in the text slice.
type fakeMediaBot struct {
	*fakeBot

	mu         sync.Mutex
	serverPath map[string]string // file_id -> server-side path
	body       map[string][]byte // server-side path -> content
	broken     map[string]bool   // server-side path -> fail the download
	requested  []string          // file_ids passed to getFile, in order
	downloads  map[string]int    // server-side path -> download count
}

// newFakeMediaBot starts the media Bot API with the given updates already queued
// for the first poll, so files can be registered before the bridge starts.
func newFakeMediaBot(t *testing.T, updates ...telegram.Update) (*fakeMediaBot, *httptest.Server) {
	t.Helper()
	base := &fakeBot{delivered: make(chan struct{}, 8)}
	if len(updates) > 0 {
		base.batches = [][]telegram.Update{updates}
	}
	b := &fakeMediaBot{
		fakeBot:    base,
		serverPath: map[string]string{},
		body:       map[string][]byte{},
		broken:     map[string]bool{},
		downloads:  map[string]int{},
	}
	srv := httptest.NewServer(http.HandlerFunc(b.route))
	t.Cleanup(srv.Close)
	return b, srv
}

// addFile registers a file_id the fake can resolve and download.
func (b *fakeMediaBot) addFile(fileID, serverPath string, body []byte) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.serverPath[fileID] = serverPath
	b.body[serverPath] = body
}

// breakDownload registers a file whose download endpoint fails, which is how a
// transport error during the stream is simulated.
func (b *fakeMediaBot) breakDownload(fileID, serverPath string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.serverPath[fileID] = serverPath
	b.broken[serverPath] = true
}

// requestedFiles lists the file_ids getFile was asked for, in order.
func (b *fakeMediaBot) requestedFiles() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]string(nil), b.requested...)
}

// downloadCount reports how many times one server-side path was downloaded.
func (b *fakeMediaBot) downloadCount(serverPath string) int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.downloads[serverPath]
}

// route serves the media routes and delegates the rest to the base fake.
func (b *fakeMediaBot) route(w http.ResponseWriter, r *http.Request) {
	switch {
	case strings.HasPrefix(r.URL.Path, "/file/bot"):
		b.serveDownload(w, r)
	case strings.HasSuffix(r.URL.Path, "/getFile"):
		b.serveGetFile(w, r)
	default:
		b.fakeBot.handle(w, r)
	}
}

func (b *fakeMediaBot) serveGetFile(w http.ResponseWriter, r *http.Request) {
	var payload struct {
		FileID string `json:"file_id"`
	}
	decodeBotJSON(r, &payload)

	b.mu.Lock()
	b.requested = append(b.requested, payload.FileID)
	path, ok := b.serverPath[payload.FileID]
	b.mu.Unlock()

	if !ok {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, `{"ok":false,"error_code":400,"description":"invalid file_id"}`)
		return
	}
	writeBotJSON(w, map[string]any{"ok": true, "result": map[string]any{"file_path": path}})
}

func (b *fakeMediaBot) serveDownload(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/file/bot"+testBotToken+"/")

	b.mu.Lock()
	b.downloads[path]++
	broken := b.broken[path]
	body := b.body[path]
	b.mu.Unlock()

	if broken {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = io.WriteString(w, `{"ok":false,"error_code":502,"description":"download failed"}`)
		return
	}
	if body == nil {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	_, _ = w.Write(body)
}

// mediaHarness is one media wiring under test.
type mediaHarness struct {
	runtime  *bridgeRuntime
	bot      *fakeMediaBot
	prompts  string
	vaultDir string
	cancel   context.CancelFunc
	finish   chan struct{}
	runErr   error
}

// startMediaBridge composes the real wiring against the media fakes and runs it
// with the given inbound attachment ceiling. The runtime keeps the store
// alongside the client it downloads into, which is what makes the size cap one
// number for both the pre-download check and the store.
func startMediaBridge(t *testing.T, bot *fakeMediaBot, srv *httptest.Server, maxInbound int64) *mediaHarness {
	t.Helper()

	prompts := filepath.Join(t.TempDir(), "prompts.jsonl")
	vaultDir := t.TempDir()
	t.Setenv(envMediaPiPromptReport, prompts)

	cfg := config.Config{
		TelegramToken:   testBotToken,
		TelegramChatID:  testOwnerID,
		TelegramAPIBase: srv.URL,
		VaultDir:        vaultDir,
		BridgeSessionID: "media-session",
		BridgeTools:     "read,grep,find,ls,save_note,append_note,send_file,ask_options",
		BridgeTimeout:   20 * time.Second,
		PiBin:           "pi",
	}

	rt, err := newBridgeRuntime(cfg, log.New(io.Discard, "", 0),
		withBridgeCommandFactory(mediaPiFactory()),
		withInboundFileLimit(maxInbound),
	)
	if err != nil {
		t.Fatalf("newBridgeRuntime: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	h := &mediaHarness{
		runtime:  rt,
		bot:      bot,
		prompts:  prompts,
		vaultDir: vaultDir,
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

// deliveries returns everything the chat received, rich messages and classic
// fallbacks alike.
func (h *mediaHarness) deliveries() []string {
	finals, messages, _, _ := h.bot.snapshot()
	return append(append([]string{}, finals...), messages...)
}

// waitFor blocks until cond holds, so a test never sleeps for a fixed guess.
func (h *mediaHarness) waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	finals, messages, drafts, typing := h.bot.snapshot()
	t.Fatalf("timed out waiting for the media turn; finals=%v classic=%v drafts=%v typing=%v prompts=%v",
		finals, messages, drafts, typing, h.readPrompts(t))
}

// readPrompts returns the prompts the fake Pi received, in order.
func (h *mediaHarness) readPrompts(t *testing.T) []string {
	t.Helper()
	raw, err := os.ReadFile(h.prompts)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatalf("read prompt record: %v", err)
	}
	var out []string
	for _, line := range strings.Split(string(raw), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var prompt string
		if err := json.Unmarshal([]byte(line), &prompt); err != nil {
			t.Fatalf("decode prompt record %q: %v", line, err)
		}
		out = append(out, prompt)
	}
	return out
}

// inboxFiles lists the file names stored under <vault>/inbox, and fails when the
// inbox does not exist yet.
func (h *mediaHarness) inboxFiles(t *testing.T) []string {
	t.Helper()
	dir := filepath.Join(h.vaultDir, "inbox")
	entries, err := os.ReadDir(dir)
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

func (h *mediaHarness) stop(t *testing.T) error {
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

// TestBridgeMediaDocumentIsStoredAndHandedToPi is the whole inbound-file slice
// for one document: downloaded exactly once, stored inside the vault inbox under
// the original name, and handed to Pi as a vault-relative path it can read.
func TestBridgeMediaDocumentIsStoredAndHandedToPi(t *testing.T) {
	const body = "contenido del informe trimestral"
	bot, srv := newFakeMediaBot(t, telegram.Update{
		ID: 1,
		Message: &telegram.Message{
			MessageID: 20,
			Chat:      telegram.Chat{ID: testOwnerID},
			Document: &telegram.Document{
				FileID:   "doc-1",
				FileName: "Informe Q1.pdf",
				MimeType: "application/pdf",
				FileSize: int64(len(body)),
			},
		},
	})
	bot.addFile("doc-1", "documents/file_1.pdf", []byte(body))
	h := startMediaBridge(t, bot, srv, testInboundCap)

	h.waitFor(t, func() bool { return len(h.deliveries()) == 1 })
	time.Sleep(50 * time.Millisecond)

	if got := bot.requestedFiles(); len(got) != 1 || got[0] != "doc-1" {
		t.Errorf("getFile file_ids = %v, want exactly [doc-1]", got)
	}
	if got := bot.downloadCount("documents/file_1.pdf"); got != 1 {
		t.Errorf("downloads = %d, want exactly one", got)
	}

	names := h.inboxFiles(t)
	if len(names) != 1 {
		t.Fatalf("inbox = %v, want one stored file", names)
	}
	name := names[0]
	if !strings.HasSuffix(name, "-Informe Q1.pdf") {
		t.Errorf("stored name = %q, want the original name after a date prefix", name)
	}
	if len(name) < len("2006-01-02") {
		t.Fatalf("stored name = %q, want a date prefix", name)
	}
	if _, err := time.Parse("2006-01-02", name[:len("2006-01-02")]); err != nil {
		t.Errorf("stored name %q does not start with an ISO date: %v", name, err)
	}

	stored := filepath.Join(h.vaultDir, "inbox", name)
	if rel, err := filepath.Rel(h.vaultDir, stored); err != nil || strings.HasPrefix(rel, "..") {
		t.Errorf("stored path %q escapes the vault root %q", stored, h.vaultDir)
	}
	got, err := os.ReadFile(stored)
	if err != nil {
		t.Fatalf("read stored file: %v", err)
	}
	if string(got) != body {
		t.Errorf("stored content = %q, want %q", got, body)
	}

	prompts := h.readPrompts(t)
	if len(prompts) != 1 {
		t.Fatalf("prompts = %v, want exactly one media prompt", prompts)
	}
	prompt := prompts[0]
	rel := filepath.ToSlash(filepath.Join("inbox", name))
	if !strings.Contains(prompt, rel) {
		t.Errorf("prompt %q does not carry the vault-relative path %q", prompt, rel)
	}
	if strings.Contains(prompt, h.vaultDir) {
		t.Errorf("prompt %q leaks the absolute vault path; Pi works from the vault root", prompt)
	}
	if !strings.Contains(prompt, "Informe Q1.pdf") {
		t.Errorf("prompt %q does not carry the original file name", prompt)
	}
	if !strings.Contains(prompt, "application/pdf") {
		t.Errorf("prompt %q does not carry the MIME type", prompt)
	}
	if !strings.Contains(prompt, "una sola pregunta") {
		t.Errorf("prompt %q must ask Pi to acknowledge and ask one short question when there is no caption", prompt)
	}

	delivered := h.deliveries()
	if len(delivered) != 1 || delivered[0] != mediaPiAnswer {
		t.Errorf("delivered %v, want the Pi answer %q through the shared turn pipeline", delivered, mediaPiAnswer)
	}
}

// TestBridgeMediaCaptionBecomesTheInstruction proves the caption is the owner's
// instruction and reaches Pi verbatim, replacing the "ask what to do" branch.
func TestBridgeMediaCaptionBecomesTheInstruction(t *testing.T) {
	const caption = "resume este PDF en tres puntos"
	bot, srv := newFakeMediaBot(t, telegram.Update{
		ID: 1,
		Message: &telegram.Message{
			MessageID: 21,
			Chat:      telegram.Chat{ID: testOwnerID},
			Caption:   caption,
			Document: &telegram.Document{
				FileID:   "doc-caption",
				FileName: "notas.pdf",
				MimeType: "application/pdf",
				FileSize: 12,
			},
		},
	})
	bot.addFile("doc-caption", "documents/file_2.pdf", []byte("doce bytes!!"))
	h := startMediaBridge(t, bot, srv, testInboundCap)

	h.waitFor(t, func() bool { return len(h.deliveries()) == 1 })

	prompts := h.readPrompts(t)
	if len(prompts) != 1 {
		t.Fatalf("prompts = %v, want exactly one media prompt", prompts)
	}
	if !strings.Contains(prompts[0], caption) {
		t.Errorf("prompt %q does not carry the caption %q", prompts[0], caption)
	}
	if strings.Contains(prompts[0], "una sola pregunta") {
		t.Errorf("prompt %q asks Pi to ask a question even though the owner gave an instruction", prompts[0])
	}
}

// TestBridgeMediaOversizedDocumentIsRefusedBeforeDownload proves a declared size
// above the cap costs no download and still answers the chat in one human line.
func TestBridgeMediaOversizedDocumentIsRefusedBeforeDownload(t *testing.T) {
	bot, srv := newFakeMediaBot(t, telegram.Update{
		ID: 1,
		Message: &telegram.Message{
			MessageID: 22,
			Chat:      telegram.Chat{ID: testOwnerID},
			Document: &telegram.Document{
				FileID:   "doc-big",
				FileName: "enorme.zip",
				MimeType: "application/zip",
				FileSize: testInboundCap + 1,
			},
		},
	})
	bot.addFile("doc-big", "documents/file_3.zip", []byte("no debería descargarse"))
	h := startMediaBridge(t, bot, srv, testInboundCap)

	h.waitFor(t, func() bool { return len(h.deliveries()) == 1 })
	time.Sleep(50 * time.Millisecond)

	if got := bot.requestedFiles(); len(got) != 0 {
		t.Errorf("getFile calls = %v, want none: an oversized document is rejected before any request", got)
	}
	if got := bot.downloadCount("documents/file_3.zip"); got != 0 {
		t.Errorf("downloads = %d, want 0 for an oversized document", got)
	}
	if prompts := h.readPrompts(t); len(prompts) != 0 {
		t.Errorf("prompts = %v, want none: Pi must not be asked about a file that was never stored", prompts)
	}
	if names := h.inboxFiles(t); len(names) != 0 {
		t.Errorf("inbox = %v, want nothing stored", names)
	}

	delivered := h.deliveries()
	if len(delivered) != 1 {
		t.Fatalf("delivered %v, want one human line", delivered)
	}
	if !strings.Contains(delivered[0], "enorme.zip") || !strings.Contains(delivered[0], "límite") {
		t.Errorf("human line %q does not name the file and the size limit", delivered[0])
	}
}

// TestBridgeMediaTooLargeDownloadIsRefusedByStore proves the store's cap is a
// second gate: a file that lies about its size (or a photo, which declares none)
// fails mid-stream with a human line instead of a partial file in the vault.
func TestBridgeMediaTooLargeDownloadIsRefusedByStore(t *testing.T) {
	big := strings.Repeat("x", int(testInboundCap)+64)
	bot, srv := newFakeMediaBot(t, telegram.Update{
		ID: 1,
		Message: &telegram.Message{
			MessageID: 23,
			Chat:      telegram.Chat{ID: testOwnerID},
			Document: &telegram.Document{
				FileID:   "doc-lying",
				FileName: "mentiroso.bin",
				MimeType: "application/octet-stream",
				// Telegram omitted the size, so only the store can catch it.
				FileSize: 0,
			},
		},
	})
	bot.addFile("doc-lying", "documents/file_4.bin", []byte(big))
	h := startMediaBridge(t, bot, srv, testInboundCap)

	h.waitFor(t, func() bool { return len(h.deliveries()) == 1 })
	time.Sleep(50 * time.Millisecond)

	if got := bot.downloadCount("documents/file_4.bin"); got != 1 {
		t.Errorf("downloads = %d, want one attempt before the store refuses", got)
	}
	if names := h.inboxFiles(t); len(names) != 0 {
		t.Errorf("inbox = %v, want the oversized file removed", names)
	}
	if prompts := h.readPrompts(t); len(prompts) != 0 {
		t.Errorf("prompts = %v, want none for a file that was not stored", prompts)
	}

	delivered := h.deliveries()
	if len(delivered) != 1 {
		t.Fatalf("delivered %v, want one human line", delivered)
	}
	if !strings.Contains(delivered[0], "mentiroso.bin") || !strings.Contains(delivered[0], "límite") {
		t.Errorf("human line %q does not explain the size limit", delivered[0])
	}
}

// TestBridgeMediaPhotoUsesTheLargestSize proves the photo path downloads the
// resolution the poller already selected (the last, largest one) and stores it
// under the synthetic photo name.
func TestBridgeMediaPhotoUsesTheLargestSize(t *testing.T) {
	const largest = "la foto grande"
	bot, srv := newFakeMediaBot(t, telegram.Update{
		ID: 1,
		Message: &telegram.Message{
			MessageID: 24,
			Chat:      telegram.Chat{ID: testOwnerID},
			Photo: []telegram.PhotoSize{
				{FileID: "size-small", Width: 90, Height: 90},
				{FileID: "size-medium", Width: 320, Height: 320},
				{FileID: "size-large", Width: 1280, Height: 1280},
			},
		},
	})
	bot.addFile("size-large", "photos/file_5.jpg", []byte(largest))
	h := startMediaBridge(t, bot, srv, testInboundCap)

	h.waitFor(t, func() bool { return len(h.deliveries()) == 1 })
	time.Sleep(50 * time.Millisecond)

	if got := bot.requestedFiles(); len(got) != 1 || got[0] != "size-large" {
		t.Fatalf("getFile file_ids = %v, want exactly [size-large]: the largest size the poller selected", got)
	}
	if got := bot.downloadCount("photos/file_5.jpg"); got != 1 {
		t.Errorf("downloads = %d, want exactly one", got)
	}

	names := h.inboxFiles(t)
	if len(names) != 1 {
		t.Fatalf("inbox = %v, want one stored photo", names)
	}
	if !strings.HasSuffix(names[0], "-photo.jpg") {
		t.Errorf("stored name = %q, want the synthetic photo name", names[0])
	}
	got, err := os.ReadFile(filepath.Join(h.vaultDir, "inbox", names[0]))
	if err != nil {
		t.Fatalf("read stored photo: %v", err)
	}
	if string(got) != largest {
		t.Errorf("stored content = %q, want the largest size %q", got, largest)
	}

	prompts := h.readPrompts(t)
	if len(prompts) != 1 {
		t.Fatalf("prompts = %v, want exactly one media prompt", prompts)
	}
	rel := filepath.ToSlash(filepath.Join("inbox", names[0]))
	if !strings.Contains(prompts[0], rel) {
		t.Errorf("prompt %q does not carry the stored photo path %q", prompts[0], rel)
	}
}

// TestBridgeMediaDownloadFailureDoesNotStopTheLoop proves a failed download is a
// human line plus a log entry, never the end of the channel: the text message
// that follows is still answered.
func TestBridgeMediaDownloadFailureDoesNotStopTheLoop(t *testing.T) {
	bot, srv := newFakeMediaBot(t,
		telegram.Update{
			ID: 1,
			Message: &telegram.Message{
				MessageID: 25,
				Chat:      telegram.Chat{ID: testOwnerID},
				Document: &telegram.Document{
					FileID:   "doc-broken",
					FileName: "roto.pdf",
					MimeType: "application/pdf",
					FileSize: 10,
				},
			},
		},
		telegram.Update{
			ID: 2,
			Message: &telegram.Message{
				MessageID: 26,
				Chat:      telegram.Chat{ID: testOwnerID},
				Text:      "sigue vivo",
			},
		},
	)
	bot.breakDownload("doc-broken", "documents/file_6.pdf")
	h := startMediaBridge(t, bot, srv, testInboundCap)

	h.waitFor(t, func() bool { return len(h.deliveries()) == 2 })

	delivered := h.deliveries()
	if delivered[0] != bridgeMediaFailedReply {
		t.Errorf("first delivery = %q, want the human failure line %q", delivered[0], bridgeMediaFailedReply)
	}
	if delivered[1] != mediaPiAnswer {
		t.Errorf("second delivery = %q, want the text turn answered with %q", delivered[1], mediaPiAnswer)
	}

	prompts := h.readPrompts(t)
	if len(prompts) != 1 {
		t.Fatalf("prompts = %v, want only the text turn: a failed download must not reach Pi", prompts)
	}
	if !strings.Contains(prompts[0], "sigue vivo") {
		t.Errorf("prompt %q does not carry the text message that followed the failure", prompts[0])
	}

	select {
	case <-h.finish:
		t.Fatalf("the poll loop stopped after a failed download: %v", h.runErr)
	default:
	}

	if err := h.stop(t); !errors.Is(err, context.Canceled) {
		t.Errorf("Run returned %v, want context.Canceled after cancellation", err)
	}
}
