package telegram

import (
	"context"
	"errors"
	"io"
	"log"
	"testing"
	"time"
)

var errApp = errors.New("boom")

type recordingClient struct {
	updates []Update
	sent    []struct {
		chatID int64
		text   string
	}
	drafts    []string
	actions   []string
	sendErr   error
	draftErr  error
	actionErr error
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

func (r *recordingClient) SendChatAction(_ context.Context, _ int64, action string) error {
	if r.actionErr != nil {
		return r.actionErr
	}
	r.actions = append(r.actions, action)
	return nil
}

func (r *recordingClient) SendMessageDraft(_ context.Context, _ int64, _ int, text string) error {
	if r.draftErr != nil {
		return r.draftErr
	}
	r.drafts = append(r.drafts, text)
	return nil
}

func newTestAdapter(client APIClient, h Handler) *Adapter {
	return NewAdapter(client, h, log.New(io.Discard, "", 0))
}

// blockingActionClient models a Telegram endpoint that never answers the cheap
// sendChatAction call. Everything else succeeds immediately.
type blockingActionClient struct{}

func (blockingActionClient) GetUpdates(context.Context, int) ([]Update, error) { return nil, nil }

func (blockingActionClient) SendMessage(context.Context, int64, string) error { return nil }

func (blockingActionClient) SendMessageDraft(context.Context, int64, int, string) error {
	return nil
}

func (blockingActionClient) SendChatAction(ctx context.Context, _ int64, _ string) error {
	<-ctx.Done()
	return ctx.Err()
}

func TestHandleUpdateCapsInitialChatActionLatency(t *testing.T) {
	client := blockingActionClient{}
	started := make(chan struct{})
	adapter := newTestAdapter(client, func(context.Context, string, Stream) (string, error) {
		close(started)
		return "ok", nil
	})

	go adapter.handleUpdate(context.Background(), Update{
		ID:      1,
		Message: &Message{Chat: Chat{ID: 9}, Text: "hola"},
	})

	// The handler must start shortly after the typing timeout expires, never
	// blocked indefinitely on the unanswered sendChatAction.
	select {
	case <-started:
	case <-time.After(2 * typingTimeout):
		t.Fatal("handler start blocked by slow sendChatAction")
	}
}

func TestHandleUpdateRepliesToOriginatingChat(t *testing.T) {
	svc := &fakeCommandService{}
	client := &recordingClient{}
	adapter := newTestAdapter(client, func(ctx context.Context, text string, stream Stream) (string, error) {
		return Handle(ctx, svc, text, stream, nil)
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
	// Cheap feedback before generating the reply.
	if len(client.actions) != 1 || client.actions[0] != "typing" {
		t.Errorf("expected one typing chat action, got %v", client.actions)
	}
}

func TestHandleUpdateStreamsDraftThenFinalizes(t *testing.T) {
	client := &recordingClient{}
	adapter := newTestAdapter(client, func(_ context.Context, _ string, stream Stream) (string, error) {
		if err := stream.Update(context.Background(), "Hola"); err != nil {
			t.Errorf("stream update: %v", err)
		}
		if err := stream.Update(context.Background(), "Hola mun"); err != nil {
			t.Errorf("stream update: %v", err)
		}
		return "Hola mundo", nil
	})

	adapter.handleUpdate(context.Background(), Update{
		ID:      9,
		Message: &Message{Chat: Chat{ID: 5}, MessageID: 42, Text: "hola"},
	})

	// The partial drafts were streamed in order...
	if len(client.drafts) != 2 || client.drafts[0] != "Hola" || client.drafts[1] != "Hola mun" {
		t.Errorf("drafts = %v, want [Hola, Hola mun]", client.drafts)
	}
	// ...and the complete reply was persisted with sendMessage.
	if len(client.sent) != 1 || client.sent[0].text != "Hola mundo" {
		t.Errorf("finalized = %v, want [Hola mundo]", client.sent)
	}
}

func TestUpdateStreamStopsTypingOnFirstDelta(t *testing.T) {
	client := &recordingClient{}
	first := 0
	s := &updateStream{
		client:       client,
		chatID:       1,
		draftID:      5,
		logger:       log.New(io.Discard, "", 0),
		onFirstDelta: func() { first++ },
	}
	for i := 0; i < 3; i++ {
		if err := s.Update(context.Background(), "x"); err != nil {
			t.Fatalf("Update: %v", err)
		}
	}
	// The typing keepalive must be signalled exactly once, on the first delta.
	if first != 1 {
		t.Errorf("onFirstDelta called %d times, want 1", first)
	}
	if len(client.drafts) != 3 {
		t.Errorf("expected 3 drafts, got %d", len(client.drafts))
	}
}

func TestHandleUpdateSkipsNonTextMessages(t *testing.T) {
	sentErr := &recordingClient{}
	adapter := newTestAdapter(sentErr, func(context.Context, string, Stream) (string, error) {
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
	adapter := newTestAdapter(client, func(context.Context, string, Stream) (string, error) {
		return "reply", nil
	})
	adapter.handleUpdate(context.Background(), Update{
		ID:      4,
		Message: &Message{Chat: Chat{ID: 1}, Text: "/nueva x"},
	})
	// A send failure must not panic; it is logged and the loop continues.
}

func TestHandleUpdateDraftFailureDoesNotBreakReply(t *testing.T) {
	client := &recordingClient{draftErr: errors.New("draft unsupported")}
	adapter := newTestAdapter(client, func(_ context.Context, _ string, stream Stream) (string, error) {
		_ = stream.Update(context.Background(), "parcial")
		return "final", nil
	})
	adapter.handleUpdate(context.Background(), Update{
		ID:      6,
		Message: &Message{Chat: Chat{ID: 3}, MessageID: 7, Text: "x"},
	})
	// The final reply must still be sent even if the draft call failed.
	if len(client.sent) != 1 || client.sent[0].text != "final" {
		t.Errorf("final reply missing after draft failure: %v", client.sent)
	}
}
