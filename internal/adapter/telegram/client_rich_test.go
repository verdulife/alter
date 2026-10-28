package telegram

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// newTestServer captures the last request and answers with a canned envelope.
func newTestServer(t *testing.T, status int, result string) (*httptest.Server, *http.Request, *[]byte) {
	t.Helper()
	var gotReq http.Request
	var gotBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotReq = *r
		body, _ := io.ReadAll(r.Body)
		gotBody = body
		w.WriteHeader(status)
		_, _ = io.WriteString(w, result)
	}))
	t.Cleanup(srv.Close)
	return srv, &gotReq, &gotBody
}

const okMessage = `{"ok":true,"result":{"message_id":42,"chat":{"id":7},"text":"hi"}}`

func TestSendRichUsesConfiguredBaseAndMarkdown(t *testing.T) {
	srv, req, body := newTestServer(t, http.StatusOK, okMessage)

	c := NewClientWithBase("TOKEN", srv.URL)
	msg, err := c.SendRich(context.Background(), 7, "# Hola\n\n| a | b |\n|---|---|\n| 1 | 2 |")
	if err != nil {
		t.Fatalf("SendRich: %v", err)
	}
	if msg.MessageID != 42 {
		t.Errorf("MessageID = %d, want 42", msg.MessageID)
	}
	if got, want := req.URL.Path, "/botTOKEN/sendRichMessage"; got != want {
		t.Errorf("path = %q, want %q", got, want)
	}
	var payload struct {
		ChatID      int64 `json:"chat_id"`
		RichMessage struct {
			Markdown string `json:"markdown"`
			HTML     string `json:"html"`
		} `json:"rich_message"`
	}
	if err := json.Unmarshal(*body, &payload); err != nil {
		t.Fatalf("decode payload: %v (body=%s)", err, *body)
	}
	if payload.ChatID != 7 {
		t.Errorf("chat_id = %d, want 7", payload.ChatID)
	}
	if !strings.Contains(payload.RichMessage.Markdown, "| a | b |") {
		t.Errorf("markdown lost table: %q", payload.RichMessage.Markdown)
	}
	if payload.RichMessage.HTML != "" {
		t.Errorf("html = %q, want empty (markdown is the only content field)", payload.RichMessage.HTML)
	}
}

func TestSendRichReturnsAPIError(t *testing.T) {
	srv, _, _ := newTestServer(t, http.StatusBadRequest,
		`{"ok":false,"error_code":400,"description":"Bad Request: RICH_MESSAGE_EMPTY"}`)

	c := NewClientWithBase("TOKEN", srv.URL)
	_, err := c.SendRich(context.Background(), 7, "")
	if err == nil {
		t.Fatal("SendRich = nil error, want APIError")
	}
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("error type = %T, want *APIError", err)
	}
	if apiErr.Code != 400 {
		t.Errorf("Code = %d, want 400", apiErr.Code)
	}
	if !strings.Contains(apiErr.Description, "RICH_MESSAGE_EMPTY") {
		t.Errorf("Description = %q, want it to carry the Telegram reason", apiErr.Description)
	}
	if !strings.Contains(err.Error(), "sendRichMessage") {
		t.Errorf("Error() = %q, want the method name", err.Error())
	}
}

func TestSendDocumentUploadsMultipartFile(t *testing.T) {
	srv, req, body := newTestServer(t, http.StatusOK, okMessage)

	dir := t.TempDir()
	path := filepath.Join(dir, "notes.md")
	if err := os.WriteFile(path, []byte("# notas\n"), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	c := NewClientWithBase("TOKEN", srv.URL)
	if err := c.SendDocument(context.Background(), 7, path, "mis notas"); err != nil {
		t.Fatalf("SendDocument: %v", err)
	}

	if got, want := req.URL.Path, "/botTOKEN/sendDocument"; got != want {
		t.Errorf("path = %q, want %q", got, want)
	}
	ct := req.Header.Get("Content-Type")
	if !strings.HasPrefix(ct, "multipart/form-data") {
		t.Fatalf("Content-Type = %q, want multipart/form-data", ct)
	}
	encoded := string(*body)
	for _, want := range []string{`name="chat_id"`, `7`, `name="caption"`, "mis notas", `name="document"`, `filename="notes.md"`, "# notas"} {
		if !strings.Contains(encoded, want) {
			t.Errorf("multipart body missing %q", want)
		}
	}
}

func TestGetFileAndDownload(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		if strings.HasPrefix(r.URL.Path, "/file/botTOKEN/") {
			_, _ = io.WriteString(w, "contenido")
			return
		}
		_, _ = io.WriteString(w, `{"ok":true,"result":{"file_id":"F1","file_path":"documents/file_1.pdf"}}`)
	}))
	t.Cleanup(srv.Close)

	c := NewClientWithBase("TOKEN", srv.URL)
	fp, err := c.GetFile(context.Background(), "F1")
	if err != nil {
		t.Fatalf("GetFile: %v", err)
	}
	if fp != "documents/file_1.pdf" {
		t.Errorf("file_path = %q, want documents/file_1.pdf", fp)
	}

	var buf strings.Builder
	if err := c.DownloadFile(context.Background(), fp, &buf); err != nil {
		t.Fatalf("DownloadFile: %v", err)
	}
	if buf.String() != "contenido" {
		t.Errorf("downloaded = %q, want contenido", buf.String())
	}
	if got, want := gotPath, "/file/botTOKEN/documents/file_1.pdf"; got != want {
		t.Errorf("download path = %q, want %q", got, want)
	}
}

func TestAnswerCallbackQueryAndChatAction(t *testing.T) {
	var paths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		_, _ = io.WriteString(w, `{"ok":true,"result":true}`)
	}))
	t.Cleanup(srv.Close)

	c := NewClientWithBase("TOKEN", srv.URL)
	if err := c.AnswerCallbackQuery(context.Background(), "CB1"); err != nil {
		t.Fatalf("AnswerCallbackQuery: %v", err)
	}
	if err := c.SendChatAction(context.Background(), 7, "typing"); err != nil {
		t.Fatalf("SendChatAction: %v", err)
	}
	want := []string{"/botTOKEN/answerCallbackQuery", "/botTOKEN/sendChatAction"}
	if len(paths) != 2 || paths[0] != want[0] || paths[1] != want[1] {
		t.Errorf("paths = %v, want %v", paths, want)
	}
}

// TestSendMessageKeepsHTMLBehaviour is the regression guard: the pre-existing
// plain send must keep using parse_mode HTML.
func TestSendMessageKeepsHTMLBehaviour(t *testing.T) {
	srv, req, body := newTestServer(t, http.StatusOK, okMessage)

	c := NewClientWithBase("TOKEN", srv.URL)
	if err := c.SendMessage(context.Background(), 7, "<b>hola</b>"); err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
	if got, want := req.URL.Path, "/botTOKEN/sendMessage"; got != want {
		t.Errorf("path = %q, want %q", got, want)
	}
	var payload map[string]any
	if err := json.Unmarshal(*body, &payload); err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	if payload["parse_mode"] != "HTML" {
		t.Errorf("parse_mode = %v, want HTML", payload["parse_mode"])
	}
}
