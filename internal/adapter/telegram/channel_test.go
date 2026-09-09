package telegram

import (
	"context"
	"testing"
)

func TestChannelSendUsesOwnerChat(t *testing.T) {
	client := &recordingClient{}
	ch := NewChannel(client, 4242)

	if ch.Name() != "telegram" {
		t.Errorf("Name() = %q, want telegram", ch.Name())
	}

	if err := ch.Send(context.Background(), "hola"); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if len(client.sent) != 1 {
		t.Fatalf("expected 1 send, got %d", len(client.sent))
	}
	if client.sent[0].chatID != 4242 {
		t.Errorf("channel delivered to chat %d, want owner 4242", client.sent[0].chatID)
	}
	if client.sent[0].text != "hola" {
		t.Errorf("channel text = %q, want hola", client.sent[0].text)
	}
}

func TestChannelPropagatesSendError(t *testing.T) {
	client := &recordingClient{}
	client.sendErr = errApp
	ch := NewChannel(client, 1)
	if err := ch.Send(context.Background(), "x"); err == nil {
		t.Fatal("expected Send to propagate the client error")
	}
}
