package telegram

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/verdu/alter/internal/domain"
)

type fakeChannel struct {
	sent []string
	err  error
}

func (f *fakeChannel) Name() string { return "fake" }

func (f *fakeChannel) Send(_ context.Context, m string) error {
	if f.err != nil {
		return f.err
	}
	f.sent = append(f.sent, m)
	return nil
}

func TestNotifyActionSendsMessage(t *testing.T) {
	ch := &fakeChannel{}
	a := NewNotifyAction(ch)

	task := domain.Task{Title: "Comprar pan", Description: "pan integral"}
	err := a.Execute(context.Background(), domain.Trigger{}, task)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if len(ch.sent) != 1 {
		t.Fatalf("expected 1 send, got %d", len(ch.sent))
	}
	if !strings.Contains(ch.sent[0], "Comprar pan") {
		t.Errorf("message missing title: %q", ch.sent[0])
	}
	if !strings.Contains(ch.sent[0], "pan integral") {
		t.Errorf("message missing description: %q", ch.sent[0])
	}
}

func TestNotifyActionChannelError(t *testing.T) {
	ch := &fakeChannel{err: errors.New("down")}
	a := NewNotifyAction(ch)

	if err := a.Execute(context.Background(), domain.Trigger{}, domain.Task{Title: "x"}); err == nil {
		t.Fatal("expected Execute to return the Channel error")
	}
	if len(ch.sent) != 0 {
		t.Errorf("no send should be recorded on failure, got %d", len(ch.sent))
	}
}
