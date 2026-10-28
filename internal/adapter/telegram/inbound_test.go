package telegram

import (
	"context"
	"errors"
	"log"
	"os"
	"testing"
)

type fakeSource struct {
	updates []Update
	err     error
}

func (f *fakeSource) GetUpdates(ctx context.Context, offset int) ([]Update, error) {
	if f.err != nil {
		return nil, f.err
	}
	out := f.updates
	f.updates = nil
	return out, nil
}

// collect returns a poller plus a pointer to the events the handler receives.
func collect(t *testing.T, ownerID int64) (*Poller, *[]Event) {
	t.Helper()
	var got []Event
	p := NewPoller(nil, ownerID, log.New(os.Stderr, "", 0), func(ctx context.Context, ev Event) error {
		got = append(got, ev)
		return nil
	})
	return p, &got
}

func TestNormalizeTextDocumentAndPhoto(t *testing.T) {
	p, got := collect(t, 7)

	p.Handle(context.Background(), Update{Message: &Message{
		Chat: Chat{ID: 7}, Text: "hola",
	}})
	p.Handle(context.Background(), Update{Message: &Message{
		Chat:     Chat{ID: 7},
		Caption:  "notas",
		Document: &Document{FileID: "F1", FileName: "notas.md", MimeType: "text/markdown", FileSize: 12},
	}})
	p.Handle(context.Background(), Update{Message: &Message{
		Chat:  Chat{ID: 7},
		Photo: []PhotoSize{{FileID: "small"}, {FileID: "big"}},
	}})

	if len(*got) != 3 {
		t.Fatalf("events = %d, want 3", len(*got))
	}
	if (*got)[0].Kind != EventText || (*got)[0].Text != "hola" {
		t.Errorf("event 0 = %+v, want text hola", (*got)[0])
	}
	doc := (*got)[1]
	if doc.Kind != EventDocument || doc.FileID != "F1" || doc.FileName != "notas.md" || doc.Caption != "notas" {
		t.Errorf("event 1 = %+v, want document F1", doc)
	}
	photo := (*got)[2]
	if photo.Kind != EventPhoto || photo.FileID != "big" {
		t.Errorf("event 2 = %+v, want the largest photo", photo)
	}
}

func TestNormalizeDropsForeignChatAndEmptyMessages(t *testing.T) {
	p, got := collect(t, 7)

	p.Handle(context.Background(), Update{Message: &Message{Chat: Chat{ID: 999}, Text: "intruso"}})
	p.Handle(context.Background(), Update{Message: &Message{Chat: Chat{ID: 7}}})
	p.Handle(context.Background(), Update{})

	if len(*got) != 0 {
		t.Errorf("events = %d (%+v), want 0", len(*got), *got)
	}
}

func TestNormalizeCallbackQuery(t *testing.T) {
	p, got := collect(t, 7)

	p.Handle(context.Background(), Update{CallbackQuery: &CallbackQuery{
		ID:      "CB1",
		Data:    "opt:2",
		From:    &User{ID: 7},
		Message: &Message{Chat: Chat{ID: 7}},
	}})

	if len(*got) != 1 {
		t.Fatalf("events = %d, want 1", len(*got))
	}
	ev := (*got)[0]
	if ev.Kind != EventCallback || ev.CallbackID != "CB1" || ev.CallbackData != "opt:2" || ev.ChatID != 7 {
		t.Errorf("event = %+v, want callback CB1 opt:2 in chat 7", ev)
	}
}

func TestHandlerErrorDoesNotKillTheLoop(t *testing.T) {
	var calls int
	p := NewPoller(nil, 7, log.New(os.Stderr, "", 0), func(ctx context.Context, ev Event) error {
		calls++
		return errors.New("boom")
	})

	p.Handle(context.Background(), Update{Message: &Message{Chat: Chat{ID: 7}, Text: "uno"}})
	p.Handle(context.Background(), Update{Message: &Message{Chat: Chat{ID: 7}, Text: "dos"}})

	if calls != 2 {
		t.Errorf("handler calls = %d, want 2 (a failure must not stop dispatch)", calls)
	}
}

func TestRunReturnsOnContextCancel(t *testing.T) {
	src := &fakeSource{}
	p := NewPoller(src, 7, log.New(os.Stderr, "", 0), func(context.Context, Event) error { return nil })

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if err := p.Run(ctx); err == nil {
		t.Error("Run = nil, want context error")
	}
}

func TestRunConsumesUpdatesAndAdvances(t *testing.T) {
	src := &fakeSource{updates: []Update{
		{ID: 10, Message: &Message{Chat: Chat{ID: 7}, Text: "uno"}},
		{ID: 11, Message: &Message{Chat: Chat{ID: 7}, Text: "dos"}},
	}}
	done := make(chan struct{}, 2)
	var got []string
	p := NewPoller(src, 7, log.New(os.Stderr, "", 0), func(ctx context.Context, ev Event) error {
		got = append(got, ev.Text)
		done <- struct{}{}
		return nil
	})

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		<-done
		<-done
		cancel()
	}()

	_ = p.Run(ctx)
	if len(got) != 2 || got[0] != "uno" || got[1] != "dos" {
		t.Errorf("events = %v, want [uno dos]", got)
	}
}
