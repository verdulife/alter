package naturalintent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/verdu/alter/internal/domain"
)

// piRunner abstracts the Pi RPC execution so tests can substitute a fake.
// In production this is satisfied by agent.PiAgent.
type piRunner interface {
	// RunPi sends a prompt to Pi and returns the assistant's response text.
	RunPi(ctx context.Context, prompt string) (string, error)
}

// PiNaturalInterpreter implements IntentInterpreter by delegating to Pi for
// natural language parsing. Pi produces JSON; Go validates and normalizes.
//
// Pi never has access to TaskService, TriggerService, SQLite, Scheduler,
// or EventStore. It only receives the user's text and returns structured JSON.
type PiNaturalInterpreter struct {
	runner piRunner
}

// NewPiNaturalInterpreter builds an interpreter backed by the given Pi runner.
func NewPiNaturalInterpreter(runner piRunner) *PiNaturalInterpreter {
	return &PiNaturalInterpreter{runner: runner}
}

// piIntentJSON is the raw JSON structure Pi is expected to produce.
// Fields are pointers so we can distinguish "not present" from "zero value".
type piIntentJSON struct {
	Action              *string       `json:"action"`
	Title               *string       `json:"title"`
	TaskRef             *string       `json:"task_ref,omitempty"`
	Reminder            *reminderJSON `json:"reminder,omitempty"`
	MissingFields       []string      `json:"missing_fields,omitempty"`
	ClarificationPrompt *string       `json:"clarification_prompt,omitempty"`
}

type reminderJSON struct {
	Relative     *string         `json:"relative,omitempty"`
	AbsoluteTime *string         `json:"absolute_time,omitempty"`
	AbsoluteDate *string         `json:"absolute_date,omitempty"`
	Recurrence   *recurrenceJSON `json:"recurrence,omitempty"`
}

// recurrenceJSON is the raw recurrence object Pi is expected to produce (B3 S4).
// All scalar fields are pointers so "absent" is distinguishable from a zero
// value; weekdays is a slice (empty = absent). Pi never emits timezone and
// never needs to know the current date.
type recurrenceJSON struct {
	Freq        *string `json:"freq,omitempty"`
	Interval    *int    `json:"interval,omitempty"`
	Time        *string `json:"time,omitempty"`
	Weekdays    []int   `json:"weekdays,omitempty"`
	DayOfMonth  *int    `json:"day_of_month,omitempty"`
	AnchorMonth *int    `json:"anchor_month,omitempty"`
	AnchorDay   *int    `json:"anchor_day,omitempty"`
	AnchorYear  *int    `json:"anchor_year,omitempty"`
}

// Interpret sends the user's text to Pi, parses the JSON response, and
// normalizes it into an IntentResult. It never mutates application state.
func (i *PiNaturalInterpreter) Interpret(ctx context.Context, text string, ictx InterpretContext) (IntentResult, error) {
	resp, err := i.runner.RunPi(ctx, systemPrompt+"\n\nUser message: "+text)
	if err != nil {
		return IntentResult{}, fmt.Errorf("natural interpreter: pi execution failed: %w", err)
	}

	return parseIntentResponse(resp)
}

// parseIntentResponse parses Pi's JSON response into an IntentResult.
// It validates the structure and normalizes the output without executing anything.
func parseIntentResponse(raw string) (IntentResult, error) {
	// Strip markdown code fences if present (some LLMs wrap JSON in ```json ... ```).
	raw = stripCodeFences(raw)
	raw = strings.TrimSpace(raw)

	if raw == "" {
		return IntentResult{Unrecognized: true}, nil
	}

	var parsed piIntentJSON
	if err := json.Unmarshal([]byte(raw), &parsed); err != nil {
		return IntentResult{}, fmt.Errorf("natural interpreter: invalid JSON from pi: %w", err)
	}

	return normalizeIntent(parsed)
}

// stripCodeFences removes markdown code fences from Pi's response.
func stripCodeFences(s string) string {
	s = strings.TrimSpace(s)
	if strings.HasPrefix(s, "```") {
		// Find the first newline after opening fence.
		if idx := strings.Index(s, "\n"); idx >= 0 {
			s = s[idx+1:]
		}
		// Remove closing fence.
		if strings.HasSuffix(s, "```") {
			s = s[:len(s)-3]
		}
		return strings.TrimSpace(s)
	}
	return s
}

// normalizeIntent converts the raw Pi JSON into a validated IntentResult.
func normalizeIntent(parsed piIntentJSON) (IntentResult, error) {
	action := derefStr(parsed.Action)

	switch action {
	case "unrecognized", "":
		return IntentResult{Unrecognized: true}, nil

	case "create_task", "create_reminder":
		return normalizeRecognizedOrAmbiguous(parsed, action)

	case "list_tasks":
		return IntentResult{
			Recognized: &RecognizedIntent{
				Action: ActionListTasks,
			},
		}, nil

	case "complete_task", "cancel_task":
		return normalizeTaskAction(parsed, action)

	default:
		return IntentResult{}, fmt.Errorf("natural interpreter: unknown action %q", action)
	}
}

// normalizeTaskAction handles complete_task and cancel_task, validating that a task_ref is present.
func normalizeTaskAction(parsed piIntentJSON, action string) (IntentResult, error) {
	taskRef := derefStr(parsed.TaskRef)
	missing := parsed.MissingFields

	// If Pi declared missing fields, it's ambiguous.
	if len(missing) > 0 {
		prompt := derefStr(parsed.ClarificationPrompt)
		if prompt == "" {
			prompt = "¿Qué tarea quieres " + actionVerb(action) + "?"
		}
		return IntentResult{
			Ambiguous: &AmbiguousIntent{
				Action:              Action(action),
				MissingFields:       missing,
				ClarificationPrompt: prompt,
			},
		}, nil
	}

	// TaskRef is required for complete/cancel.
	if taskRef == "" {
		return IntentResult{
			Ambiguous: &AmbiguousIntent{
				Action:              Action(action),
				MissingFields:       []string{"task_ref"},
				ClarificationPrompt: "¿Qué tarea quieres " + actionVerb(action) + "?",
			},
		}, nil
	}

	return IntentResult{
		Recognized: &RecognizedIntent{
			Action:  Action(action),
			TaskRef: taskRef,
		},
	}, nil
}

// actionVerb returns a human-readable verb for the action's clarification prompt.
func actionVerb(action string) string {
	switch action {
	case "complete_task":
		return "completar"
	case "cancel_task":
		return "cancelar"
	default:
		return "procesar"
	}
}

// normalizeRecognizedOrAmbiguous handles create_task and create_reminder,
// checking whether all required fields are present.
func normalizeRecognizedOrAmbiguous(parsed piIntentJSON, action string) (IntentResult, error) {
	title := derefStr(parsed.Title)
	missing := parsed.MissingFields

	// If Pi declared missing fields, it's ambiguous.
	if len(missing) > 0 {
		prompt := derefStr(parsed.ClarificationPrompt)
		if prompt == "" {
			prompt = "¿Puedes darme más detalles?"
		}
		return IntentResult{
			Ambiguous: &AmbiguousIntent{
				Action:              Action(action),
				MissingFields:       missing,
				CandidateTitle:      title,
				ClarificationPrompt: prompt,
			},
		}, nil
	}

	// Title is required for both actions.
	if title == "" {
		return IntentResult{
			Ambiguous: &AmbiguousIntent{
				Action:              Action(action),
				MissingFields:       []string{"title"},
				ClarificationPrompt: "¿Qué tarea quieres crear?",
			},
		}, nil
	}

	// For create_task without reminder, we're done.
	if action == string(ActionCreateTask) && parsed.Reminder == nil {
		return IntentResult{
			Recognized: &RecognizedIntent{
				Action: ActionCreateTask,
				Title:  title,
			},
		}, nil
	}

	// For create_reminder (or create_task with a reminder spec), validate the reminder.
	if parsed.Reminder != nil {
		spec, err := normalizeReminderSpec(parsed.Reminder)
		if err != nil {
			return IntentResult{
				Ambiguous: &AmbiguousIntent{
					Action:              ActionCreateReminder,
					MissingFields:       []string{"time"},
					CandidateTitle:      title,
					ClarificationPrompt: fmt.Sprintf("No pude entender el horario: %v. ¿Cuándo quieres el recordatorio?", err),
				},
			}, nil
		}
		return IntentResult{
			Recognized: &RecognizedIntent{
				Action:   ActionCreateReminder,
				Title:    title,
				Reminder: spec,
			},
		}, nil
	}

	// create_task but Pi added reminder-like fields? Treat as task only.
	// (This shouldn't happen with a good prompt, but be safe.)
	return IntentResult{
		Recognized: &RecognizedIntent{
			Action: ActionCreateTask,
			Title:  title,
		},
	}, nil
}

// normalizeReminderSpec validates and normalizes a reminder spec from Pi's JSON.
func normalizeReminderSpec(r *reminderJSON) (*ReminderSpec, error) {
	spec := &ReminderSpec{}

	rel := derefStr(r.Relative)
	absTime := derefStr(r.AbsoluteTime)
	absDate := derefStr(r.AbsoluteDate)

	// Recurrence is mutually exclusive with the one-shot forms: never guess.
	if r.Recurrence != nil && (rel != "" || absTime != "") {
		return nil, fmt.Errorf("ambiguous: recurrence cannot be combined with a one-shot time")
	}

	if r.Recurrence != nil {
		rec, err := normalizeRecurrence(r.Recurrence)
		if err != nil {
			return nil, err
		}
		spec.Recurrence = rec
		return spec, nil
	}

	if rel != "" && absTime != "" {
		return nil, fmt.Errorf("ambiguous: both relative and absolute time specified")
	}

	if rel != "" {
		// Validate it's a parseable Go duration.
		spec.Relative = rel
		return spec, nil
	}

	if absTime != "" {
		spec.AbsoluteTime = absTime
		spec.AbsoluteDate = absDate // may be empty → defaults to "today" in resolver
		return spec, nil
	}

	return nil, fmt.Errorf("no time specified in reminder")
}

// normalizeRecurrence validates the structural fields Pi produced and maps them
// into RecurrenceParams (B3 S4). Absent or contradictory data is an error, so
// the caller turns it into an AmbiguousIntent: Go never invents cadence data.
func normalizeRecurrence(r *recurrenceJSON) (*RecurrenceParams, error) {
	p := &RecurrenceParams{}

	freqStr := derefStr(r.Freq)
	switch domain.RecurrenceFreq(freqStr) {
	case domain.RecurrenceFreqDaily, domain.RecurrenceFreqWeekly, domain.RecurrenceFreqMonthly, domain.RecurrenceFreqYearly:
		p.Freq = domain.RecurrenceFreq(freqStr)
	default:
		return nil, fmt.Errorf("recurrence frequency is required (daily|weekly|monthly|yearly)")
	}

	timeStr := derefStr(r.Time)
	if timeStr == "" {
		return nil, fmt.Errorf("recurrence time is required")
	}
	p.Time = timeStr

	if r.Interval != nil {
		if *r.Interval < 1 {
			return nil, fmt.Errorf("recurrence interval must be >= 1")
		}
		p.Interval = r.Interval
	}

	// Weekdays: only meaningful for weekly; any non-empty value elsewhere is
	// contradictory, and values outside 1..7 are rejected.
	for _, wd := range r.Weekdays {
		if wd < 1 || wd > 7 {
			return nil, fmt.Errorf("recurrence weekday out of range 1..7")
		}
	}
	if p.Freq == domain.RecurrenceFreqWeekly {
		if len(r.Weekdays) == 0 {
			return nil, fmt.Errorf("weekly recurrence requires weekdays")
		}
		p.Weekdays = r.Weekdays
	} else if len(r.Weekdays) > 0 {
		return nil, fmt.Errorf("recurrence weekdays only apply to weekly")
	}

	// DayOfMonth: only for monthly.
	if p.Freq == domain.RecurrenceFreqMonthly {
		if r.DayOfMonth == nil || *r.DayOfMonth < 1 || *r.DayOfMonth > 31 {
			return nil, fmt.Errorf("monthly recurrence requires day_of_month in 1..31")
		}
		p.DayOfMonth = r.DayOfMonth
	} else if r.DayOfMonth != nil {
		return nil, fmt.Errorf("recurrence day_of_month only applies to monthly")
	}

	// Anchor month/day together; required for yearly.
	if (r.AnchorMonth == nil) != (r.AnchorDay == nil) {
		return nil, fmt.Errorf("recurrence anchor month and day must be provided together")
	}
	if r.AnchorMonth != nil && (*r.AnchorMonth < 1 || *r.AnchorMonth > 12) {
		return nil, fmt.Errorf("recurrence anchor month out of range 1..12")
	}
	if r.AnchorDay != nil && (*r.AnchorDay < 1 || *r.AnchorDay > 31) {
		return nil, fmt.Errorf("recurrence anchor day out of range 1..31")
	}
	if p.Freq == domain.RecurrenceFreqYearly {
		if r.AnchorMonth == nil {
			return nil, fmt.Errorf("yearly recurrence requires an anchor month and day")
		}
	}
	// For non-yearly frequencies the user may still state an explicit start date
	// ("a partir del lunes 9 de marzo"): the components are kept as-is.
	p.AnchorMonth, p.AnchorDay = r.AnchorMonth, r.AnchorDay
	if r.AnchorYear != nil {
		if *r.AnchorYear < 1 {
			return nil, fmt.Errorf("recurrence anchor year out of range")
		}
		p.AnchorYear = r.AnchorYear
	}

	return p, nil
}

// derefStr returns the string value of a pointer, or "" if nil.
func derefStr(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
