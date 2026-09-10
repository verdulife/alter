package telegram

import (
	"context"
	"errors"
	"io"
	"log"
	"testing"
)

var errApp = errors.New("boom")

type recordingClient struct {
	updates []Update
	sent    []struct {
		chatID int64
		text   string
	}
	sendErr error
}

func (r *recordingClient) GetUpdates(context.Context, int) ([]Update, error) { return r.updates, nil }

func (r *recordingClient) SendMessage(_ context.Context, chatID int64, text string) error {
	if r.sendErr != nil {
		return r.sendErr
	}
	r.sent = append(r.sent, struct {
		chatID int64
		text   string
	}{chatID, text})
	return nil
}

func newTestAdapter(client APIClient, h Handler) *Adapter {
	return NewAdapter(client, h, log.New(io.Discard, "", 0))
}

func TestHandleUpdateRepliesToOriginatingChat(t *testing.T) {
	svc := &fakeCommandService{}
	client := &recordingClient{}
	adapter := newTestAdapter(client, func(ctx context.Context, text string) (string, error) {
		return Handle(ctx, svc, text, nil)
	})

	adapter.handleUpdate(context.Background(), Update{
		ID: 1,
		Message: &Message{
			Chat: Chat{ID: 77},
			Text: "/nueva regar plantas",
		},
	})

	if len(client.sent) != 1 {
		t.Fatalf("expected 1 reply, got %d", len(client.sent))
	}
	if client.sent[0].chatID != 77 {
		t.Errorf("reply chat = %d, want 77 (originating chat)", client.sent[0].chatID)
	}
	if client.sent[0].text == "" {
		t.Error("reply text should not be empty")
	}
}

func TestHandleUpdateSkipsNonTextMessages(t *testing.T) {
	sentErr := &recordingClient{}
	adapter := newTestAdapter(sentErr, func(context.Context, string) (string, error) {
		return "unexpected", nil
	})

	// A message without text (e.g. a photo) must produce no reply.
	adapter.handleUpdate(context.Background(), Update{ID: 2, Message: &Message{Chat: Chat{ID: 5}}})
	if len(sentErr.sent) != 0 {
		t.Errorf("expected no reply for empty text, got %d", len(sentErr.sent))
	}

	// A nil message (e.g. a callback query or edited message) must be ignored.
	adapter.handleUpdate(context.Background(), Update{ID: 3, Message: nil})
	if len(sentErr.sent) != 0 {
		t.Errorf("expected no reply for nil message, got %d", len(sentErr.sent))
	}
}

func TestHandleUpdateLogsSendFailureWithoutPanic(t *testing.T) {
	client := &recordingClient{sendErr: errors.New("network down")}
	adapter := newTestAdapter(client, func(context.Context, string) (string, error) {
		return "reply", nil
	})
	adapter.handleUpdate(context.Background(), Update{
		ID:      4,
		Message: &Message{Chat: Chat{ID: 1}, Text: "/nueva x"},
	})
	// A send failure must not panic; it is logged and the loop continues.
}
