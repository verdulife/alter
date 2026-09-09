package telegram

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// methodOf extracts the trailing method segment, e.g. "sendMessage" from
// "/bot<token>/sendMessage".
func methodOf(path string) string {
	i := strings.LastIndex(path, "/")
	return path[i+1:]
}

func TestClientSendMessageAndGetUpdates(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch methodOf(r.URL.Path) {
		case "getUpdates":
			if err := r.Body.Close(); err != nil {
				t.Errorf("close body: %v", err)
			}
			w.Write([]byte(`{"ok":true,"result":[{"update_id":10,"message":{"message_id":1,"chat":{"id":7},"text":"/nueva x"}}]}`))
		case "sendMessage":
			var payload map[string]any
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Errorf("decode body: %v", err)
			}
			if payload["chat_id"] != float64(7) || payload["text"] != "hola" {
				t.Errorf("unexpected payload: %+v", payload)
			}
			w.Write([]byte(`{"ok":true,"result":{"message_id":2,"chat":{"id":7},"text":"ok"}}`))
		default:
			http.Error(w, "unknown method", http.StatusBadRequest)
		}
	}))
	defer server.Close()

	c := NewClient("tok")
	c.baseURL = server.URL
	ctx := context.Background()

	updates, err := c.GetUpdates(ctx, 5)
	if err != nil {
		t.Fatalf("GetUpdates: %v", err)
	}
	if len(updates) != 1 || updates[0].ID != 10 {
		t.Fatalf("updates = %+v, want one update id 10", updates)
	}
	if updates[0].Message.Chat.ID != 7 || updates[0].Message.Text != "/nueva x" {
		t.Errorf("unexpected message: %+v", updates[0].Message)
	}

	if err := c.SendMessage(ctx, 7, "hola"); err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
}

func TestClientReportsApiError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"ok":false,"description":"Unauthorized"}`))
	}))
	defer server.Close()

	c := NewClient("tok")
	c.baseURL = server.URL

	if err := c.SendMessage(context.Background(), 1, "x"); err == nil {
		t.Fatal("expected SendMessage to surface the API error")
	}
}
