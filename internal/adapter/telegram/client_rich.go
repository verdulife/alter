package telegram

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// APIError is a Bot API failure carrying the method, the HTTP/Telegram error
// code and the server's description. It is exported so callers can distinguish
// a rejected feature (e.g. a rich message Telegram refuses) from a transport
// failure and degrade accordingly.
type APIError struct {
	Method      string
	Code        int
	Description string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("telegram: %s failed: %s", e.Method, e.Description)
}

// inputRichMessage is the wire shape of Telegram's InputRichMessage. Exactly one
// of markdown, html or blocks must be present; this client sends markdown,
// which is GFM-compatible and therefore accepts Pi's output verbatim.
type inputRichMessage struct {
	Markdown string `json:"markdown,omitempty"`
	HTML     string `json:"html,omitempty"`
}

// NewClientWithBase builds a Client against an explicit Bot API base URL.
// Passing an empty base keeps the public cloud API. Pointing it at a
// self-hosted telegram-bot-api server (for example http://127.0.0.1:8081)
// switches upload/download limits without any other code change.
func NewClientWithBase(token, baseURL string) *Client {
	c := NewClient(token)
	if baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/"); baseURL != "" {
		c.baseURL = baseURL
	}
	return c
}

// SendRich sends a rich message whose body is the given markdown. Telegram
// renders headings, lists, task lists, tables, quotations, footnotes, formulas
// and <details> natively, up to 32768 characters and 500 blocks per message.
func (c *Client) SendRich(ctx context.Context, chatID int64, markdown string) (Message, error) {
	var msg Message
	err := c.call(ctx, "sendRichMessage", map[string]any{
		"chat_id":       chatID,
		"rich_message": inputRichMessage{Markdown: markdown},
	}, &msg)
	return msg, err
}

// SendRichDraft streams a partial rich message into an ephemeral draft while the
// answer is still being generated. The draft is replaced by the final
// SendRich/SendMessage call.
func (c *Client) SendRichDraft(ctx context.Context, chatID int64, draftID int, markdown string) error {
	return c.call(ctx, "sendRichMessageDraft", map[string]any{
		"chat_id":       chatID,
		"draft_id":      draftID,
		"rich_message": inputRichMessage{Markdown: markdown},
	}, nil)
}

// SendChatAction emits transient chat state feedback ("typing", "upload_document",
// ...). Telegram expires it after a few seconds, so callers refresh it.
func (c *Client) SendChatAction(ctx context.Context, chatID int64, action string) error {
	return c.call(ctx, "sendChatAction", map[string]any{
		"chat_id": chatID,
		"action":  action,
	}, nil)
}

// AnswerCallbackQuery clears the client-side spinner after a button press.
func (c *Client) AnswerCallbackQuery(ctx context.Context, callbackID string) error {
	return c.call(ctx, "answerCallbackQuery", map[string]any{
		"callback_query_id": callbackID,
	}, nil)
}

// SendDocument uploads a local file as a document. Rich message media blocks
// accept only HTTP/HTTPS URLs, so local vault files always travel this way.
func (c *Client) SendDocument(ctx context.Context, chatID int64, path, caption string) error {
	return c.sendFile(ctx, "sendDocument", "document", chatID, path, caption)
}

// SendPhoto uploads a local image as a photo.
func (c *Client) SendPhoto(ctx context.Context, chatID int64, path, caption string) error {
	return c.sendFile(ctx, "sendPhoto", "photo", chatID, path, caption)
}

func (c *Client) sendFile(ctx context.Context, method, field string, chatID int64, path, caption string) error {
	fields := map[string]string{"chat_id": fmt.Sprintf("%d", chatID)}
	if caption != "" {
		fields["caption"] = caption
	}
	return c.callMultipart(ctx, method, fields, field, path, nil)
}

// GetFile resolves a file_id to its server-side path. In --local mode a
// self-hosted server may return an absolute local path, which is still valid
// for DownloadFile in the same machine.
func (c *Client) GetFile(ctx context.Context, fileID string) (string, error) {
	var result struct {
		FilePath string `json:"file_path"`
	}
	if err := c.call(ctx, "getFile", map[string]any{"file_id": fileID}, &result); err != nil {
		return "", err
	}
	return result.FilePath, nil
}

// DownloadFile streams a previously resolved file object to w.
func (c *Client) DownloadFile(ctx context.Context, filePath string, w io.Writer) error {
	url := fmt.Sprintf("%s/file/bot%s/%s", c.baseURL, c.token, strings.TrimLeft(filePath, "/"))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return &APIError{Method: "downloadFile", Code: resp.StatusCode, Description: resp.Status}
	}
	_, err = io.Copy(w, resp.Body)
	return err
}

// callMultipart performs a multipart/form-data POST. It is used for uploads,
// which the Bot API does not accept as JSON.
func (c *Client) callMultipart(ctx context.Context, method string, fields map[string]string, fileField, filePath string, out any) error {
	file, err := os.Open(filePath)
	if err != nil {
		return fmt.Errorf("telegram: open upload %q: %w", filePath, err)
	}
	defer file.Close()

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	for k, v := range fields {
		if err := writer.WriteField(k, v); err != nil {
			return err
		}
	}
	part, err := writer.CreateFormFile(fileField, filepath.Base(filePath))
	if err != nil {
		return err
	}
	if _, err := io.Copy(part, file); err != nil {
		return err
	}
	if err := writer.Close(); err != nil {
		return err
	}

	url := fmt.Sprintf("%s/bot%s/%s", c.baseURL, c.token, method)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body.Bytes()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())

	// Uploads are slower than JSON calls; the default client timeout is tuned for
	// polling, so give the transfer its own budget.
	client := &http.Client{Timeout: 10 * time.Minute}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	return decodeEnvelope(resp, method, out)
}
