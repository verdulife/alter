package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/verdu/alter/internal/vault"
)

const (
	saveNotePath   = "/tools/save_note"
	appendNotePath = "/tools/append_note"
	sendFilePath   = "/tools/send_file"
	askOptionsPath = "/tools/ask_options"
)

// sentFile records one delivery the server asked the sender to make.
type sentFile struct {
	path    string
	caption string
}

// fakeSender is the FileSender the tests inject: it never touches Telegram.
type fakeSender struct {
	calls []sentFile
	err   error
}

func (f *fakeSender) SendFile(_ context.Context, path, caption string) error {
	f.calls = append(f.calls, sentFile{path: path, caption: caption})
	return f.err
}

// fakeAsker is the OptionAsker the tests inject. block makes it wait for the
// context, the way the real asker waits for the owner's button press.
type fakeAsker struct {
	calls       int
	question    string
	options     []string
	answer      string
	err         error
	block       bool
	sawDeadline bool
}

func (f *fakeAsker) AskOptions(ctx context.Context, question string, options []string) (string, error) {
	f.calls++
	f.question = question
	f.options = append([]string(nil), options...)
	_, f.sawDeadline = ctx.Deadline()
	if f.block {
		<-ctx.Done()
		return "", ctx.Err()
	}
	return f.answer, f.err
}

// harness wires a real Server around a temporary vault and the fakes.
type harness struct {
	server *Server
	// dir is the vault root; parent is the directory just outside it.
	dir    string
	parent string
	base   string
	files  *fakeSender
	asker  *fakeAsker
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	return newHarnessWith(t)
}

// newHarnessWith builds a harness whose vault lives in a fresh temporary
// directory, optionally tuning the Config before construction.
func newHarnessWith(t *testing.T, opts ...func(*Config)) *harness {
	t.Helper()
	root := t.TempDir()
	return newHarnessAt(t, filepath.Join(root, "vault"), opts...)
}

func newHarnessAt(t *testing.T, vaultDir string, opts ...func(*Config)) *harness {
	t.Helper()
	if err := os.MkdirAll(vaultDir, 0o755); err != nil {
		t.Fatalf("create vault dir: %v", err)
	}
	h := &harness{
		dir:    vaultDir,
		parent: filepath.Dir(vaultDir),
		files:  &fakeSender{},
		asker:  &fakeAsker{answer: "Sí, adelante"},
	}
	cfg := Config{
		Vault:      vault.New(vaultDir, 0),
		Files:      h.files,
		Asker:      h.asker,
		Logger:     log.New(io.Discard, "", 0),
		AskTimeout: time.Minute,
	}
	for _, opt := range opts {
		opt(&cfg)
	}
	srv, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	h.server = srv
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	h.base = ts.URL
	return h
}

// response is one decoded reply from the server.
type response struct {
	status int
	body   map[string]any
	raw    string
}

// do posts payload to endpoint with the given token. An empty token means "send
// no header at all", which is what a rogue local process would do.
func (h *harness) do(t *testing.T, endpoint, token string, payload any) response {
	t.Helper()
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	req, err := http.NewRequest(http.MethodPost, h.base+endpoint, bytes.NewReader(body))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set(tokenHeader, token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("post %s: %v", endpoint, err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read %s response: %v", endpoint, err)
	}
	out := response{status: resp.StatusCode, raw: string(raw)}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &out.body); err != nil {
			t.Fatalf("%s replied with non-JSON %q", endpoint, raw)
		}
	}
	return out
}

// post is an authenticated call, the way the Pi extension makes it.
func (h *harness) post(t *testing.T, endpoint string, payload any) response {
	t.Helper()
	return h.do(t, endpoint, h.server.Token(), payload)
}

func (r response) str(t *testing.T, key string) string {
	t.Helper()
	v, ok := r.body[key].(string)
	if !ok {
		t.Fatalf("body %q has no string field %q", r.raw, key)
	}
	return v
}

func (r response) boolean(t *testing.T, key string) bool {
	t.Helper()
	v, ok := r.body[key].(bool)
	if !ok {
		t.Fatalf("body %q has no bool field %q", r.raw, key)
	}
	return v
}

// errorText asserts the reply carries the {"error": "..."} business shape.
func (r response) errorText(t *testing.T) string {
	t.Helper()
	if r.status < 400 || r.status > 499 {
		t.Fatalf("status = %d, want a 4xx business error (body %q)", r.status, r.raw)
	}
	msg, ok := r.body["error"].(string)
	if !ok || strings.TrimSpace(msg) == "" {
		t.Fatalf("body %q has no non-empty error field", r.raw)
	}
	return msg
}

// insideVault reports the vault-relative form of path, failing when it escapes.
func insideVault(t *testing.T, root, path string) string {
	t.Helper()
	rel, err := filepath.Rel(root, path)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		t.Fatalf("path %q is outside the vault %q", path, root)
	}
	return rel
}

func writeVaultFile(t *testing.T, root, rel, content string) string {
	t.Helper()
	full := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatalf("create %s: %v", filepath.Dir(full), err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", full, err)
	}
	return full
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(data)
}

// vaultEntries lists every file and directory under the vault root so a
// rejected request can be proven to have written nothing.
func vaultEntries(t *testing.T, root string) []string {
	t.Helper()
	var out []string
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if path == root {
			return nil
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return relErr
		}
		out = append(out, rel)
		return nil
	})
	if err != nil {
		t.Fatalf("walk vault %s: %v", root, err)
	}
	return out
}

func TestSaveNoteWritesTheNoteAndReturnsTheRelativePath(t *testing.T) {
	h := newHarness(t)

	res := h.post(t, saveNotePath, map[string]any{
		"title":  "Nota de prueba",
		"body":   "Cuerpo de la nota.",
		"folder": "alter",
		"tags":   []string{"alter", "t7"},
	})
	if res.status != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %q)", res.status, res.raw)
	}
	want := filepath.Join("alter", "nota-de-prueba.md")
	if got := res.str(t, "path"); got != want {
		t.Errorf("path = %q, want %q", got, want)
	}

	content := readFile(t, filepath.Join(h.dir, want))
	for _, fragment := range []string{"title: Nota de prueba", "# Nota de prueba", "Cuerpo de la nota.", "- alter", "- t7"} {
		if !strings.Contains(content, fragment) {
			t.Errorf("note does not contain %q:\n%s", fragment, content)
		}
	}
}

func TestSaveNoteRejectsAFolderThatEscapesTheVault(t *testing.T) {
	h := newHarness(t)

	res := h.post(t, saveNotePath, map[string]any{
		"title":  "Escapa",
		"body":   "no debería escribirse",
		"folder": "..",
	})
	res.errorText(t)

	if entries := vaultEntries(t, h.dir); len(entries) != 0 {
		t.Errorf("vault has %v after a rejected save, want it empty", entries)
	}
	if _, err := os.Stat(filepath.Join(h.parent, "escapa.md")); err == nil {
		t.Errorf("rejected save wrote %s", filepath.Join(h.parent, "escapa.md"))
	}
}

func TestSaveNoteRejectsATitleThatEscapesTheVault(t *testing.T) {
	h := newHarness(t)

	res := h.post(t, saveNotePath, map[string]any{
		"title": "../../evil",
		"body":  "no debería escribirse",
	})
	res.errorText(t)

	if entries := vaultEntries(t, h.dir); len(entries) != 0 {
		t.Errorf("vault has %v after a rejected save, want it empty", entries)
	}
	if _, err := os.Stat(filepath.Join(h.parent, "evil.md")); err == nil {
		t.Errorf("rejected save wrote %s", filepath.Join(h.parent, "evil.md"))
	}
}

func TestAppendNoteAddsOneLineToAnExistingNote(t *testing.T) {
	h := newHarness(t)
	rel := filepath.Join("alter", "ideas.md")
	path := writeVaultFile(t, h.dir, rel, "# Ideas\n\n- primera")

	res := h.post(t, appendNotePath, map[string]any{"note": rel, "line": "- segunda"})
	if res.status != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %q)", res.status, res.raw)
	}
	if got := res.str(t, "path"); got != rel {
		t.Errorf("path = %q, want %q", got, rel)
	}
	if got, want := readFile(t, path), "# Ideas\n\n- primera\n- segunda\n"; got != want {
		t.Errorf("note = %q, want %q", got, want)
	}
}

func TestAppendNoteRejectsAPathOutsideTheVault(t *testing.T) {
	h := newHarness(t)
	outside := writeVaultFile(t, h.parent, "fuera.md", "intacto\n")

	res := h.post(t, appendNotePath, map[string]any{"note": "../fuera.md", "line": "- inyectada"})
	res.errorText(t)

	if got := readFile(t, outside); got != "intacto\n" {
		t.Errorf("file outside the vault was modified: %q", got)
	}
}

func TestSendFileRejectsAPathOutsideTheVaultEvenIfItExists(t *testing.T) {
	h := newHarness(t)
	outside := writeVaultFile(t, h.parent, "fuera.pdf", "secreto")

	res := h.post(t, sendFilePath, map[string]any{"path": "../fuera.pdf", "caption": "no viajes"})
	res.errorText(t)

	if len(h.files.calls) != 0 {
		t.Errorf("sender was called %d times for a path outside the vault", len(h.files.calls))
	}
	if got := readFile(t, outside); got != "secreto" {
		t.Errorf("file outside the vault changed: %q", got)
	}
}

func TestSendFileRejectsASymlinkLeavingTheVault(t *testing.T) {
	h := newHarness(t)
	outside := writeVaultFile(t, h.parent, "secreto.txt", "secreto")
	if err := os.Symlink(outside, filepath.Join(h.dir, "enlace.txt")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	res := h.post(t, sendFilePath, map[string]any{"path": "enlace.txt"})
	res.errorText(t)

	if len(h.files.calls) != 0 {
		t.Errorf("sender was called %d times for a symlink leaving the vault", len(h.files.calls))
	}
}

func TestSendFileSendsAVaultFileWithItsCaption(t *testing.T) {
	h := newHarness(t)
	rel := filepath.Join("alter", "informe.pdf")
	path := writeVaultFile(t, h.dir, rel, "%PDF-1.4")

	res := h.post(t, sendFilePath, map[string]any{"path": rel, "caption": "Informe de T7"})
	if res.status != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %q)", res.status, res.raw)
	}
	if got := res.str(t, "path"); got != rel {
		t.Errorf("path = %q, want %q", got, rel)
	}
	if len(h.files.calls) != 1 {
		t.Fatalf("sender calls = %d, want 1", len(h.files.calls))
	}
	call := h.files.calls[0]
	if got := insideVault(t, h.dir, call.path); got != rel {
		t.Errorf("sender path = %q, want %q", got, rel)
	}
	if call.caption != "Informe de T7" {
		t.Errorf("caption = %q, want %q", call.caption, "Informe de T7")
	}
	if got, want := readFile(t, path), "%PDF-1.4"; got != want {
		t.Errorf("file content = %q, want %q", got, want)
	}
}

func TestAskOptionsRejectsAnOptionCountOutsideTwoToFour(t *testing.T) {
	for _, tc := range []struct {
		name    string
		options []string
	}{
		{"none", nil},
		{"one", []string{"solo"}},
		{"five", []string{"a", "b", "c", "d", "e"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)

			res := h.post(t, askOptionsPath, map[string]any{"question": "¿Qué hago?", "options": tc.options})
			res.errorText(t)

			if h.asker.calls != 0 {
				t.Errorf("asker calls = %d, want 0 for an invalid request", h.asker.calls)
			}
		})
	}
}

func TestAskOptionsReturnsTheChosenOption(t *testing.T) {
	h := newHarness(t)
	question := "¿Guardo la nota en alter o en inbox?"
	options := []string{"alter", "inbox", "ninguna"}

	res := h.post(t, askOptionsPath, map[string]any{"question": question, "options": options})
	if res.status != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %q)", res.status, res.raw)
	}
	if got := res.str(t, "answer"); got != "Sí, adelante" {
		t.Errorf("answer = %q, want the asker's choice", got)
	}
	if res.boolean(t, "timed_out") {
		t.Error("timed_out = true, want false for a real answer")
	}
	if h.asker.calls != 1 {
		t.Fatalf("asker calls = %d, want 1", h.asker.calls)
	}
	if h.asker.question != question {
		t.Errorf("question = %q, want %q", h.asker.question, question)
	}
	if strings.Join(h.asker.options, ",") != strings.Join(options, ",") {
		t.Errorf("options = %v, want %v", h.asker.options, options)
	}
}

func TestAskOptionsWithoutAnswerIsNotAnError(t *testing.T) {
	h := newHarnessWith(t, func(cfg *Config) { cfg.AskTimeout = 5 * time.Second })
	h.asker.err = ErrNoAnswer

	res := h.post(t, askOptionsPath, map[string]any{"question": "¿Sigo?", "options": []string{"sí", "no"}})
	if res.status != http.StatusOK {
		t.Fatalf("status = %d, want 200 with a no-answer result (body %q)", res.status, res.raw)
	}
	if got := strings.ToLower(res.str(t, "answer")); !strings.Contains(got, "sin respuesta") {
		t.Errorf("answer = %q, want a clear \"sin respuesta\" result", got)
	}
	if !res.boolean(t, "timed_out") {
		t.Error("timed_out = false, want true when the owner does not answer")
	}
}

func TestAskOptionsBoundsTheWaitWithItsOwnDeadline(t *testing.T) {
	h := newHarnessWith(t, func(cfg *Config) { cfg.AskTimeout = 100 * time.Millisecond })
	h.asker.block = true

	start := time.Now()
	res := h.post(t, askOptionsPath, map[string]any{"question": "¿Sigo?", "options": []string{"sí", "no"}})
	elapsed := time.Since(start)

	if res.status != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %q)", res.status, res.raw)
	}
	if !res.boolean(t, "timed_out") {
		t.Error("timed_out = false, want true after the deadline")
	}
	if !h.asker.sawDeadline {
		t.Error("asker received no deadline, want the ask timeout applied to its context")
	}
	if elapsed > 10*time.Second {
		t.Errorf("ask_options took %s, want it bounded by the ask timeout", elapsed)
	}
}

func TestEveryEndpointRequiresTheToken(t *testing.T) {
	requests := map[string]any{
		saveNotePath:   map[string]any{"title": "Token", "body": "cuerpo"},
		appendNotePath: map[string]any{"note": filepath.Join("alter", "ideas.md"), "line": "- inyectada"},
		sendFilePath:   map[string]any{"path": filepath.Join("alter", "informe.pdf"), "caption": "x"},
		askOptionsPath: map[string]any{"question": "¿Sigo?", "options": []string{"sí", "no"}},
	}
	tokens := []struct {
		name  string
		value string
	}{
		{"missing", ""},
		{"wrong", "no-es-el-token-del-proceso"},
	}

	for endpoint, payload := range requests {
		for _, token := range tokens {
			t.Run(endpoint+"/"+token.name, func(t *testing.T) {
				h := newHarness(t)
				notePath := writeVaultFile(t, h.dir, filepath.Join("alter", "ideas.md"), "# Ideas\n")
				writeVaultFile(t, h.dir, filepath.Join("alter", "informe.pdf"), "%PDF-1.4")

				res := h.do(t, endpoint, token.value, payload)
				if res.status != http.StatusForbidden {
					t.Fatalf("status = %d, want 403 (body %q)", res.status, res.raw)
				}
				if msg, ok := res.body["error"].(string); !ok || msg == "" {
					t.Errorf("body %q has no error field", res.raw)
				}

				if got := readFile(t, notePath); got != "# Ideas\n" {
					t.Errorf("rejected request modified a note: %q", got)
				}
				if len(h.files.calls) != 0 {
					t.Errorf("rejected request called the sender %d times", len(h.files.calls))
				}
				if h.asker.calls != 0 {
					t.Errorf("rejected request called the asker %d times", h.asker.calls)
				}
				if entries := vaultEntries(t, h.dir); len(entries) != 3 {
					t.Errorf("vault has %v after a rejected request, want the seeded folder and files only", entries)
				}
			})
		}
	}
}

func TestStartBindsLoopbackOnly(t *testing.T) {
	srv, err := New(Config{
		Vault:  vault.New(t.TempDir(), 0),
		Files:  &fakeSender{},
		Asker:  &fakeAsker{answer: "sí"},
		Logger: log.New(io.Discard, "", 0),
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	url, err := srv.Start()
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = srv.Close() })

	if !strings.HasPrefix(url, "http://127.0.0.1:") {
		t.Fatalf("url = %q, want a loopback base URL", url)
	}
	if got := len(srv.Token()); got != 64 {
		t.Errorf("token length = %d, want 64 hex characters", got)
	}
	other, err := New(Config{Vault: vault.New(t.TempDir(), 0), Files: &fakeSender{}, Asker: &fakeAsker{}})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if other.Token() == srv.Token() {
		t.Error("two servers share a token, want a random token per process")
	}

	client := &http.Client{Timeout: 5 * time.Second}
	body := strings.NewReader(`{"title":"Desde el puerto","body":"cuerpo"}`)
	req, err := http.NewRequest(http.MethodPost, url+saveNotePath, body)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(tokenHeader, srv.Token())
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("post to %s: %v", url, err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d over the bound listener, want 200", resp.StatusCode)
	}

	if err := srv.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if _, err := client.Post(url+saveNotePath, "application/json", strings.NewReader(`{}`)); err == nil {
		t.Error("the server still accepts connections after Close")
	}
}

func TestNewRequiresItsDependencies(t *testing.T) {
	for _, tc := range []struct {
		name string
		cfg  Config
	}{
		{"no vault", Config{Files: &fakeSender{}, Asker: &fakeAsker{}}},
		{"no sender", Config{Vault: vault.New(t.TempDir(), 0), Asker: &fakeAsker{}}},
		{"no asker", Config{Vault: vault.New(t.TempDir(), 0), Files: &fakeSender{}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := New(tc.cfg); err == nil {
				t.Fatal("New accepted an incomplete Config, want an error")
			}
		})
	}
}

// errAskFailed is an asker failure that is not a missing answer; it must surface
// as an error the model can report instead of a fake choice.
var errAskFailed = errors.New("telegram: sendRichMessage failed")

func TestAskOptionsReportsAskerFailuresAsErrors(t *testing.T) {
	h := newHarness(t)
	h.asker.err = errAskFailed

	res := h.post(t, askOptionsPath, map[string]any{"question": "¿Sigo?", "options": []string{"sí", "no"}})
	if res.status < 400 {
		t.Fatalf("status = %d, want an error status (body %q)", res.status, res.raw)
	}
	if msg, ok := res.body["error"].(string); !ok || !strings.Contains(msg, "sendRichMessage") {
		t.Errorf("body %q does not report the asker failure", res.raw)
	}
	if _, ok := res.body["answer"]; ok {
		t.Errorf("body %q reports an answer for a failed ask", res.raw)
	}
}
