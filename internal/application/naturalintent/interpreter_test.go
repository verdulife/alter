package naturalintent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// fakePiRunner is a test double that returns pre-configured responses.
type fakePiRunner struct {
	response string
	err      error
	lastPrompt string
}

func (f *fakePiRunner) RunPi(_ context.Context, prompt string) (string, error) {
	f.lastPrompt = prompt
	if f.err != nil {
		return "", f.err
	}
	return f.response, nil
}

func TestInterpretCreateTask(t *testing.T) {
	runner := &fakePiRunner{
		response: `{"action":"create_task","title":"comprar SSD"}`,
	}
	interpreter := NewPiNaturalInterpreter(runner)
	ctx := InterpretContext{Now: time.Now(), Timezone: time.UTC}

	result, err := interpreter.Interpret(context.Background(), "comprar SSD", ctx)
	if err != nil {
		t.Fatalf("Interpret() error = %v", err)
	}
	if result.Recognized == nil {
		t.Fatal("expected Recognized intent")
	}
	if result.Recognized.Action != ActionCreateTask {
		t.Errorf("action = %q, want %q", result.Recognized.Action, ActionCreateTask)
	}
	if result.Recognized.Title != "comprar SSD" {
		t.Errorf("title = %q, want %q", result.Recognized.Title, "comprar SSD")
	}
	if result.Recognized.Reminder != nil {
		t.Error("expected no reminder for create_task")
	}
}

func TestInterpretCreateReminderRelative(t *testing.T) {
	runner := &fakePiRunner{
		response: `{"action":"create_reminder","title":"comprar SSD","reminder":{"relative":"30m"}}`,
	}
	interpreter := NewPiNaturalInterpreter(runner)
	ctx := InterpretContext{Now: time.Now(), Timezone: time.UTC}

	result, err := interpreter.Interpret(context.Background(), "recuérdame comprar SSD en 30 minutos", ctx)
	if err != nil {
		t.Fatalf("Interpret() error = %v", err)
	}
	if result.Recognized == nil {
		t.Fatal("expected Recognized intent")
	}
	if result.Recognized.Action != ActionCreateReminder {
		t.Errorf("action = %q, want %q", result.Recognized.Action, ActionCreateReminder)
	}
	if result.Recognized.Reminder == nil {
		t.Fatal("expected reminder spec")
	}
	if result.Recognized.Reminder.Relative != "30m" {
		t.Errorf("relative = %q, want %q", result.Recognized.Reminder.Relative, "30m")
	}
}

func TestInterpretCreateReminderAbsolute(t *testing.T) {
	runner := &fakePiRunner{
		response: `{"action":"create_reminder","title":"comprar SSD","reminder":{"absolute_time":"20:00","absolute_date":"today"}}`,
	}
	interpreter := NewPiNaturalInterpreter(runner)
	ctx := InterpretContext{Now: time.Now(), Timezone: time.UTC}

	result, err := interpreter.Interpret(context.Background(), "comprar SSD hoy a las 20h", ctx)
	if err != nil {
		t.Fatalf("Interpret() error = %v", err)
	}
	if result.Recognized == nil {
		t.Fatal("expected Recognized intent")
	}
	if result.Recognized.Action != ActionCreateReminder {
		t.Errorf("action = %q, want %q", result.Recognized.Action, ActionCreateReminder)
	}
	if result.Recognized.Reminder == nil {
		t.Fatal("expected reminder spec")
	}
	if result.Recognized.Reminder.AbsoluteTime != "20:00" {
		t.Errorf("absolute_time = %q, want %q", result.Recognized.Reminder.AbsoluteTime, "20:00")
	}
	if result.Recognized.Reminder.AbsoluteDate != "today" {
		t.Errorf("absolute_date = %q, want %q", result.Recognized.Reminder.AbsoluteDate, "today")
	}
}

func TestInterpretUnrecognized(t *testing.T) {
	runner := &fakePiRunner{
		response: `{"action":"unrecognized"}`,
	}
	interpreter := NewPiNaturalInterpreter(runner)
	ctx := InterpretContext{Now: time.Now(), Timezone: time.UTC}

	result, err := interpreter.Interpret(context.Background(), "hola", ctx)
	if err != nil {
		t.Fatalf("Interpret() error = %v", err)
	}
	if !result.Unrecognized {
		t.Error("expected Unrecognized to be true")
	}
	if result.Recognized != nil {
		t.Error("expected no Recognized intent")
	}
	if result.Ambiguous != nil {
		t.Error("expected no Ambiguous intent")
	}
}

func TestInterpretAmbiguousMissingTitle(t *testing.T) {
	runner := &fakePiRunner{
		response: `{"action":"create_task","missing_fields":["title"],"clarification_prompt":"¿Qué tarea quieres crear?"}`,
	}
	interpreter := NewPiNaturalInterpreter(runner)
	ctx := InterpretContext{Now: time.Now(), Timezone: time.UTC}

	result, err := interpreter.Interpret(context.Background(), "poné una tarea", ctx)
	if err != nil {
		t.Fatalf("Interpret() error = %v", err)
	}
	if result.Ambiguous == nil {
		t.Fatal("expected Ambiguous intent")
	}
	if result.Ambiguous.Action != ActionCreateTask {
		t.Errorf("action = %q, want %q", result.Ambiguous.Action, ActionCreateTask)
	}
	if len(result.Ambiguous.MissingFields) != 1 || result.Ambiguous.MissingFields[0] != "title" {
		t.Errorf("missing_fields = %v, want [title]", result.Ambiguous.MissingFields)
	}
	if result.Ambiguous.ClarificationPrompt != "¿Qué tarea quieres crear?" {
		t.Errorf("clarification_prompt = %q", result.Ambiguous.ClarificationPrompt)
	}
}

func TestInterpretAmbiguousMissingTime(t *testing.T) {
	runner := &fakePiRunner{
		response: `{"action":"create_reminder","title":"algo del SSD","missing_fields":["time"],"clarification_prompt":"¿Cuándo quieres el recordatorio?"}`,
	}
	interpreter := NewPiNaturalInterpreter(runner)
	ctx := InterpretContext{Now: time.Now(), Timezone: time.UTC}

	result, err := interpreter.Interpret(context.Background(), "recordame algo del SSD", ctx)
	if err != nil {
		t.Fatalf("Interpret() error = %v", err)
	}
	if result.Ambiguous == nil {
		t.Fatal("expected Ambiguous intent")
	}
	if result.Ambiguous.Action != ActionCreateReminder {
		t.Errorf("action = %q, want %q", result.Ambiguous.Action, ActionCreateReminder)
	}
	if len(result.Ambiguous.MissingFields) != 1 || result.Ambiguous.MissingFields[0] != "time" {
		t.Errorf("missing_fields = %v, want [time]", result.Ambiguous.MissingFields)
	}
}

func TestInterpretInvalidJSON(t *testing.T) {
	runner := &fakePiRunner{
		response: `this is not json`,
	}
	interpreter := NewPiNaturalInterpreter(runner)
	ctx := InterpretContext{Now: time.Now(), Timezone: time.UTC}

	_, err := interpreter.Interpret(context.Background(), "test", ctx)
	if err == nil {
		t.Error("expected error for invalid JSON")
	}
	if !strings.Contains(err.Error(), "invalid JSON") {
		t.Errorf("error = %q, want 'invalid JSON'", err.Error())
	}
}

func TestInterpretEmptyResponse(t *testing.T) {
	runner := &fakePiRunner{
		response: "",
	}
	interpreter := NewPiNaturalInterpreter(runner)
	ctx := InterpretContext{Now: time.Now(), Timezone: time.UTC}

	result, err := interpreter.Interpret(context.Background(), "test", ctx)
	if err != nil {
		t.Fatalf("Interpret() error = %v", err)
	}
	if !result.Unrecognized {
		t.Error("empty response should be Unrecognized")
	}
}

func TestInterpretPiError(t *testing.T) {
	runner := &fakePiRunner{
		err: context.DeadlineExceeded,
	}
	interpreter := NewPiNaturalInterpreter(runner)
	ctx := InterpretContext{Now: time.Now(), Timezone: time.UTC}

	_, err := interpreter.Interpret(context.Background(), "test", ctx)
	if err == nil {
		t.Error("expected error when Pi fails")
	}
	if !strings.Contains(err.Error(), "pi execution failed") {
		t.Errorf("error = %q, want 'pi execution failed'", err.Error())
	}
}

func TestInterpretMarkdownFencedJSON(t *testing.T) {
	runner := &fakePiRunner{
		response: "```json\n{\"action\":\"create_task\",\"title\":\"test\"}\n```",
	}
	interpreter := NewPiNaturalInterpreter(runner)
	ctx := InterpretContext{Now: time.Now(), Timezone: time.UTC}

	result, err := interpreter.Interpret(context.Background(), "test", ctx)
	if err != nil {
		t.Fatalf("Interpret() error = %v", err)
	}
	if result.Recognized == nil {
		t.Fatal("expected Recognized intent")
	}
	if result.Recognized.Title != "test" {
		t.Errorf("title = %q, want %q", result.Recognized.Title, "test")
	}
}

func TestInterpretUnknownAction(t *testing.T) {
	runner := &fakePiRunner{
		response: `{"action":"delete_all_tasks"}`,
	}
	interpreter := NewPiNaturalInterpreter(runner)
	ctx := InterpretContext{Now: time.Now(), Timezone: time.UTC}

	_, err := interpreter.Interpret(context.Background(), "borra todo", ctx)
	if err == nil {
		t.Error("expected error for unknown action")
	}
	if !strings.Contains(err.Error(), "unknown action") {
		t.Errorf("error = %q, want 'unknown action'", err.Error())
	}
}

func TestInterpretAmbiguousBothRelativeAndAbsolute(t *testing.T) {
	runner := &fakePiRunner{
		response: `{"action":"create_reminder","title":"test","reminder":{"relative":"30m","absolute_time":"20:00"}}`,
	}
	interpreter := NewPiNaturalInterpreter(runner)
	ctx := InterpretContext{Now: time.Now(), Timezone: time.UTC}

	result, err := interpreter.Interpret(context.Background(), "test", ctx)
	if err != nil {
		t.Fatalf("Interpret() error = %v", err)
	}
	// Should be ambiguous because the reminder spec is ambiguous
	if result.Ambiguous == nil {
		t.Fatal("expected Ambiguous intent for conflicting reminder spec")
	}
}

func TestInterpretReminderNoTimeFields(t *testing.T) {
	runner := &fakePiRunner{
		response: `{"action":"create_reminder","title":"test","reminder":{}}`,
	}
	interpreter := NewPiNaturalInterpreter(runner)
	ctx := InterpretContext{Now: time.Now(), Timezone: time.UTC}

	result, err := interpreter.Interpret(context.Background(), "test", ctx)
	if err != nil {
		t.Fatalf("Interpret() error = %v", err)
	}
	// Should be ambiguous because reminder has no time
	if result.Ambiguous == nil {
		t.Fatal("expected Ambiguous intent for reminder with no time")
	}
}

func TestInterpretPassesUserTextToPi(t *testing.T) {
	runner := &fakePiRunner{
		response: `{"action":"unrecognized"}`,
	}
	interpreter := NewPiNaturalInterpreter(runner)
	ctx := InterpretContext{Now: time.Now(), Timezone: time.UTC}

	userText := "comprar leche"
	_, _ = interpreter.Interpret(context.Background(), userText, ctx)

	if !strings.Contains(runner.lastPrompt, userText) {
		t.Errorf("prompt does not contain user text: %q", runner.lastPrompt)
	}
}

func TestInterpretPromptContainsSchema(t *testing.T) {
	runner := &fakePiRunner{
		response: `{"action":"unrecognized"}`,
	}
	interpreter := NewPiNaturalInterpreter(runner)
	ctx := InterpretContext{Now: time.Now(), Timezone: time.UTC}

	_, _ = interpreter.Interpret(context.Background(), "test", ctx)

	// The prompt should contain the schema definition
	if !strings.Contains(runner.lastPrompt, "create_task") {
		t.Error("prompt should contain 'create_task' from schema")
	}
	if !strings.Contains(runner.lastPrompt, "create_reminder") {
		t.Error("prompt should contain 'create_reminder' from schema")
	}
}

func TestParseIntentResponseVarious(t *testing.T) {
	tests := []struct {
		name       string
		json       string
		wantState  string // "recognized", "ambiguous", "unrecognized", "error"
		wantAction Action
	}{
		{
			name:      "valid create_task",
			json:      `{"action":"create_task","title":"test"}`,
			wantState: "recognized",
			wantAction: ActionCreateTask,
		},
		{
			name:      "valid create_reminder with relative",
			json:      `{"action":"create_reminder","title":"test","reminder":{"relative":"1h"}}`,
			wantState: "recognized",
			wantAction: ActionCreateReminder,
		},
		{
			name:      "unrecognized",
			json:      `{"action":"unrecognized"}`,
			wantState: "unrecognized",
		},
		{
			name:      "missing title",
			json:      `{"action":"create_task","missing_fields":["title"]}`,
			wantState: "ambiguous",
			wantAction: ActionCreateTask,
		},
		{
			name:      "empty action",
			json:      `{"action":"","title":"test"}`,
			wantState: "unrecognized",
		},
		{
			name:      "missing action field",
			json:      `{"title":"test"}`,
			wantState: "unrecognized",
		},
		{
			name:       "invalid json",
			json:       `{bad json`,
			wantState:  "error",
		},
		{
			name:       "null action",
			json:       `{"action":null}`,
			wantState:  "unrecognized",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := parseIntentResponse(tt.json)
			if tt.wantState == "error" {
				if err == nil {
					t.Error("expected error")
				}
				return
			}
			if err != nil {
				t.Fatalf("parseIntentResponse() error = %v", err)
			}

			switch tt.wantState {
			case "recognized":
				if result.Recognized == nil {
					t.Fatal("expected Recognized")
				}
				if result.Recognized.Action != tt.wantAction {
					t.Errorf("action = %q, want %q", result.Recognized.Action, tt.wantAction)
				}
			case "ambiguous":
				if result.Ambiguous == nil {
					t.Fatal("expected Ambiguous")
				}
			case "unrecognized":
				if !result.Unrecognized {
					t.Error("expected Unrecognized")
				}
			}
		})
	}
}

func TestNormalizeReminderSpec(t *testing.T) {
	tests := []struct {
		name    string
		spec    *reminderJSON
		wantRel string
		wantAbs string
		wantErr bool
	}{
		{
			name:    "relative only",
			spec:    &reminderJSON{Relative: strPtr("30m")},
			wantRel: "30m",
		},
		{
			name:    "absolute time only",
			spec:    &reminderJSON{AbsoluteTime: strPtr("20:00")},
			wantAbs: "20:00",
		},
		{
			name:    "absolute time and date",
			spec:    &reminderJSON{AbsoluteTime: strPtr("09:00"), AbsoluteDate: strPtr("tomorrow")},
			wantAbs: "09:00",
		},
		{
			name:    "both relative and absolute",
			spec:    &reminderJSON{Relative: strPtr("30m"), AbsoluteTime: strPtr("20:00")},
			wantErr: true,
		},
		{
			name:    "no time fields",
			spec:    &reminderJSON{},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := normalizeReminderSpec(tt.spec)
			if (err != nil) != tt.wantErr {
				t.Errorf("normalizeReminderSpec() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !tt.wantErr {
				if got.Relative != tt.wantRel {
					t.Errorf("Relative = %q, want %q", got.Relative, tt.wantRel)
				}
				if got.AbsoluteTime != tt.wantAbs {
					t.Errorf("AbsoluteTime = %q, want %q", got.AbsoluteTime, tt.wantAbs)
				}
			}
		})
	}
}

func strPtr(s string) *string { return &s }

// Verify piIntentJSON can be marshaled/unmarshaled for round-trip tests.
func TestPiIntentJSONRoundTrip(t *testing.T) {
	original := piIntentJSON{
		Action: strPtr("create_reminder"),
		Title:  strPtr("comprar SSD"),
		Reminder: &reminderJSON{
			Relative:     strPtr("30m"),
			AbsoluteTime: nil,
			AbsoluteDate: nil,
		},
		MissingFields:       nil,
		ClarificationPrompt: nil,
	}

	data, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("Marshal error: %v", err)
	}

	var decoded piIntentJSON
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("Unmarshal error: %v", err)
	}

	if *decoded.Action != *original.Action {
		t.Errorf("action = %q, want %q", *decoded.Action, *original.Action)
	}
	if *decoded.Title != *original.Title {
		t.Errorf("title = %q, want %q", *decoded.Title, *original.Title)
	}
	if decoded.Reminder == nil || *decoded.Reminder.Relative != "30m" {
		t.Errorf("reminder.relative = %v, want 30m", decoded.Reminder)
	}
}
