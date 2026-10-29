package telegram

import (
	"context"
	"strings"
	"testing"
)

// TestTransportErrorDoesNotLeakTheToken pins a credential rule: net/http wraps
// transport failures in *url.Error, whose message embeds the request URL, and a
// Bot API URL carries /bot<token>/. Logging that raw error writes the bot token
// to the journal (observed in production on 2026-10-29).
func TestTransportErrorDoesNotLeakTheToken(t *testing.T) {
	const token = "123456:SECRET-TOKEN-VALUE"
	// Port 1 on loopback refuses connections immediately: a deterministic
	// transport failure with no network dependency.
	c := NewClientWithBase(token, "http://127.0.0.1:1")

	if _, err := c.SendRich(context.Background(), 7, "hola"); err == nil {
		t.Fatal("SendRich against a closed port = nil error, want a transport failure")
	} else if strings.Contains(err.Error(), token) {
		t.Errorf("error leaks the bot token: %v", err)
	}

	if _, err := c.GetFile(context.Background(), "F1"); err == nil {
		t.Fatal("GetFile against a closed port = nil error, want a transport failure")
	} else if strings.Contains(err.Error(), token) {
		t.Errorf("GetFile error leaks the bot token: %v", err)
	}
}
