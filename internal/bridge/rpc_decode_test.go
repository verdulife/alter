package bridge

import (
	"bytes"
	"context"
	"log"
	"strings"
	"testing"
)

// TestModelEventsWithObjectMessageAreNotDropped pins the decode fix: model
// events carry `message` as an object (message_end, turn_end, message_start),
// while dialogs carry it as a string. Decoding it into a string field made the
// whole record fail and produced a "malformed RPC line" per event in production.
func TestModelEventsWithObjectMessageAreNotDropped(t *testing.T) {
	var logs bytes.Buffer
	s := NewSession(Config{}, WithLogger(log.New(&logs, "", 0)))

	for _, line := range []string{
		`{"type":"message_end","message":{"role":"assistant","content":"hola"}}`,
		`{"type":"turn_end","message":{"role":"assistant","content":"hola"},"toolResults":[]}`,
		`{"type":"message_update","usage":{},"assistantMessageEvent":{"type":"text_delta","contentIndex":0,"delta":"ho"}}`,
	} {
		if _, err := s.handleLine(context.Background(), []byte(line), &runState{}, nil); err != nil {
			t.Fatalf("handleLine(%s): %v", line, err)
		}
	}
	if strings.Contains(logs.String(), "malformed") {
		t.Errorf("a model event was dropped as malformed:\n%s", logs.String())
	}
}

// TestDialogMessageStillDecodesAsText is the regression guard for the fix: the
// dialog field must keep reaching the handler as plain text.
func TestDialogMessageStillDecodesAsText(t *testing.T) {
	var got DialogRequest
	s := NewSession(Config{
		OnDialog: func(ctx context.Context, req DialogRequest) (DialogResponse, error) {
			got = req
			return DialogResponse{Confirmed: true}, nil
		},
	}, WithLogger(log.New(&bytes.Buffer{}, "", 0)))

	line := `{"type":"extension_ui_request","id":"u1","method":"confirm","title":"Aprobación","message":"¿Ejecutar?"}`
	if _, err := s.handleLine(context.Background(), []byte(line), &runState{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("handleLine: %v", err)
	}
	if got.Message != "¿Ejecutar?" {
		t.Errorf("dialog message = %q, want ¿Ejecutar?", got.Message)
	}
	if got.Title != "Aprobación" {
		t.Errorf("dialog title = %q, want Aprobación", got.Title)
	}
}

// TestStreamedDeltasAccumulateFromTheDocumentedShape proves the wire shape the
// docs specify for message_update actually feeds the draft.
func TestStreamedDeltasAccumulateFromTheDocumentedShape(t *testing.T) {
	s := NewSession(Config{}, WithLogger(log.New(&bytes.Buffer{}, "", 0)))

	var drafts []string
	st := &runState{delta: func(text string) { drafts = append(drafts, text) }}
	for _, delta := range []string{"Ho", "la", " mundo"} {
		line := `{"type":"message_update","usage":{},"assistantMessageEvent":{"type":"text_delta","contentIndex":0,"delta":"` + delta + `"}}`
		if _, err := s.handleLine(context.Background(), []byte(line), st, nil); err != nil {
			t.Fatalf("handleLine: %v", err)
		}
	}
	if st.draft != "Hola mundo" {
		t.Errorf("draft = %q, want Hola mundo", st.draft)
	}
	if len(drafts) != 3 || drafts[2] != "Hola mundo" {
		t.Errorf("drafts = %v, want three accumulating steps", drafts)
	}
}
