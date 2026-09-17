// Package telegram implements the Telegram adapters for alter: an inbound
// transport (polling Bot API updates and replying to commands) and an outbound
// notification transport (domain.Channel).
//
// Dependency direction: this package depends only on internal/domain (for the
// Channel contract) and standard library HTTP. It never imports the scheduler
// or storage packages.
package telegram

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// Telegram wire types (the subset of the Bot API the adapter needs).
type Update struct {
	ID      int      `json:"update_id"`
	Message *Message `json:"message"`
}

type Message struct {
	MessageID int    `json:"message_id"`
	Chat      Chat   `json:"chat"`
	Text      string `json:"text"`
}

type Chat struct {
	ID int64 `json:"id"`
}

// APIClient is the minimal Telegram Bot API surface the adapter needs. It is a
// seam so tests can substitute a fake transport.
type APIClient interface {
	GetUpdates(ctx context.Context, offset int) ([]Update, error)
	SendMessage(ctx context.Context, chatID int64, text string) error
	// SendChatAction shows a transient chat action (e.g. "typing") as cheap
	// feedback while a reply is being generated.
	SendChatAction(ctx context.Context, chatID int64, action string) error
	// SendMessageDraft streams a partial message to a private chat (Bot API
	// sendMessageDraft). The draft is an ephemeral preview that the SAME draftID
	// animates across updates; the final message is persisted by SendMessage.
	SendMessageDraft(ctx context.Context, chatID int64, draftID int, text string) error
}

// Client is an HTTP-backed APIClient for the Telegram Bot API.
type Client struct {
	token   string
	baseURL string
	http    *http.Client
}

// NewClient builds a Client for the given bot token.
func NewClient(token string) *Client {
	return &Client{
		token:   token,
		baseURL: "https://api.telegram.org",
		http:    &http.Client{Timeout: 40 * time.Second},
	}
}

// GetUpdates long-polls for updates starting after the given offset.
func (c *Client) GetUpdates(ctx context.Context, offset int) ([]Update, error) {
	var result []Update
	if err := c.call(ctx, "getUpdates", map[string]any{
		"offset":  offset,
		"timeout": 20,
	}, &result); err != nil {
		return nil, err
	}
	return result, nil
}

// SendMessage posts a text message to the given chat. The message is sent with
// parse_mode "HTML" so the adapter can use <b>, <i>, etc. for formatting.
func (c *Client) SendMessage(ctx context.Context, chatID int64, text string) error {
	var result Message
	if err := c.call(ctx, "sendMessage", map[string]any{
		"chat_id":    chatID,
		"text":       text,
		"parse_mode": "HTML",
	}, &result); err != nil {
		return err
	}
	return nil
}

// SendChatAction shows a transient chat action (Bot API sendChatAction), e.g.
// "typing", to give feedback while the reply is generated.
func (c *Client) SendChatAction(ctx context.Context, chatID int64, action string) error {
	return c.call(ctx, "sendChatAction", map[string]any{
		"chat_id": chatID,
		"action":  action,
	}, nil)
}

// SendMessageDraft streams a partial message to a private chat (Bot API
// sendMessageDraft). The draft is an ephemeral ~30s preview; the client must
// call SendMessage with the complete text to persist it. draftID must be
// non-zero and stable across updates of the same draft so Telegram animates the
// transition instead of replacing it.
func (c *Client) SendMessageDraft(ctx context.Context, chatID int64, draftID int, text string) error {
	var result bool
	return c.call(ctx, "sendMessageDraft", map[string]any{
		"chat_id":  chatID,
		"draft_id": draftID,
		"text":     text,
	}, &result)
}

// call performs a JSON POST to a Bot API method and decodes the result envelope.
func (c *Client) call(ctx context.Context, method string, payload map[string]any, out any) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}

	url := fmt.Sprintf("%s/bot%s/%s", c.baseURL, c.token, method)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}

	var envelope struct {
		OK          bool            `json:"ok"`
		Description string          `json:"description"`
		Result      json.RawMessage `json:"result"`
	}
	if err := json.Unmarshal(data, &envelope); err != nil {
		return fmt.Errorf("telegram: decode response: %w", err)
	}
	if !envelope.OK {
		return fmt.Errorf("telegram: %s failed: %s", method, envelope.Description)
	}
	if out != nil && len(envelope.Result) > 0 {
		if err := json.Unmarshal(envelope.Result, out); err != nil {
			return fmt.Errorf("telegram: decode %s result: %w", method, err)
		}
	}
	return nil
}
