package bridge

import (
	"bytes"
	"context"
	"testing"
)

// The dialog answers are the last line of the approval gate (T13/L15): whatever
// Pi asks, the bridge must never invent a consent. These three cases pin that
// from the reader outward, through the public prompt path.

// TestDialogUnknownBlockingMethodGetsNoAnswer keeps the reader honest for a
// dialog method it does not know: writing nothing is the fail-closed outcome
// (Pi keeps the extension blocked), while guessing an answer would not be.
// Covers L15/T13.
func TestDialogUnknownBlockingMethodGetsNoAnswer(t *testing.T) {
	called := false
	s := NewSession(Config{OnDialog: func(context.Context, DialogRequest) (DialogResponse, error) {
		called = true
		return DialogResponse{Confirmed: true}, nil
	}})
	defer s.Close()

	out := &bytes.Buffer{}
	line := dialogRecord(t, "ui-unknown", "multiselect", map[string]any{"title": "¿Cuál?"})
	if _, err := s.handleLine(context.Background(), line, &runState{}, out); err != nil {
		t.Fatalf("handleLine: %v", err)
	}
	if called {
		t.Error("OnDialog was called for a method that is not a known dialog")
	}
	if out.Len() != 0 {
		t.Errorf("bridge wrote %q, want nothing for an unknown method", out.String())
	}
}

// TestDialogEmptyIDIsEchoedVerbatim pins the only thing the bridge can do with a
// request that carries no id: echo it back unchanged, so a response can never be
// mistaken for the answer to another dialog. Covers L15/T13.
func TestDialogEmptyIDIsEchoedVerbatim(t *testing.T) {
	cfg := Config{OnDialog: func(context.Context, DialogRequest) (DialogResponse, error) {
		return DialogResponse{Confirmed: true}, nil
	}}
	resp, err := answerDialog(t, cfg, context.Background(), dialogRecord(t, "", "confirm", map[string]any{"title": "¿Ejecutar bash?"}))
	if err != nil {
		t.Fatalf("handleLine: %v", err)
	}
	if id, ok := resp["id"]; !ok || id != "" {
		t.Errorf("response id = %v, want the empty id echoed", id)
	}
}

// TestConfirmAnswerWithoutAFilledFieldDenies proves the confirm record can never
// turn into an approval by omission: a response that carries no confirmation is
// written as confirmed:false, never as true and never as a bare record Pi could
// read as consent. Covers L15/T13.
func TestConfirmAnswerWithoutAFilledFieldDenies(t *testing.T) {
	cfg := Config{OnDialog: func(context.Context, DialogRequest) (DialogResponse, error) {
		return DialogResponse{Value: "Yes"}, nil
	}}
	resp, err := answerDialog(t, cfg, context.Background(), dialogRecord(t, "ui-c", "confirm", map[string]any{"title": "¿Ejecutar bash?"}))
	if err != nil {
		t.Fatalf("handleLine: %v", err)
	}
	if resp["confirmed"] != false {
		t.Errorf("response = %v, want confirmed:false", resp)
	}
	if _, ok := resp["cancelled"]; ok {
		t.Errorf("response = %v, want an explicit denial, not a cancellation", resp)
	}
}
