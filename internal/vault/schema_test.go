package vault

import (
	"strings"
	"testing"
	"time"
)

// The fixtures below are the four shapes the contract in
// .agents/skills/alter-vault/SKILL.md allows, with the exact field order the
// skill uses. They are documents, not files: Parse only inspects the path's
// basename to check that id and file name agree.
const (
	validTaskDoc = `---
id: 2026-10-29-renovar-dni
type: task
title: "Renovar el DNI"
status: open
priority: high
created: 2026-10-29T09:12:00+01:00
due: 2026-11-15T10:00:00+01:00
tags: "personal,documentos"
---

# Renovar el DNI

Pedir cita en la comisaría de la calle X. Llevar foto y el DNI caducado.
`

	validReminderDoc = `---
id: 2026-10-29-llamar-a-mama
type: reminder
title: "Llamar a mamá"
status: open
priority: normal
created: 2026-10-29T09:15:00+01:00
remind_at: 2026-10-30T19:00:00+01:00
recurrence: weekly
tags: "familia"
---

# Llamar a mamá

Preguntar por la revisión del médico.
`

	validEventDoc = `---
id: 2026-10-29-viaje-a-sevilla
type: event
title: "Viaje a Sevilla"
status: open
priority: normal
created: 2026-10-29T10:00:00+01:00
due: 2026-11-02T08:30:00+01:00
tags: "viajes"
---

# Viaje a Sevilla

Salida a las 08:30 desde Atocha.
`

	// The note omits status and priority on purpose: both are optional for a note
	// and the validator has to fill the documented defaults.
	validNoteDoc = `---
id: 2026-10-29-lista-de-libros
type: note
title: "Lista de libros"
created: 2026-10-29T11:00:00+01:00
tags: ""
---

# Lista de libros

- El nombre de la rosa
`
)

const validTaskPath = "/vault/tasks/2026-10-29-renovar-dni.md"

// TestParseAcceptsTheFourEntryTypes pins the happy path of the contract: one
// valid file per type parses with no problems and with the typed fields the
// caller needs, including the documented defaults (status open, priority
// normal) and an empty inline tag list.
func TestParseAcceptsTheFourEntryTypes(t *testing.T) {
	tests := []struct {
		name         string
		path         string
		doc          string
		wantType     EntryType
		wantID       string
		wantTitle    string
		wantStatus   string
		wantPriority string
		wantCreated  string
		wantDue      string
		wantRemindAt string
		wantRecur    string
		wantTags     []string
	}{
		{
			name:         "task",
			path:         validTaskPath,
			doc:          validTaskDoc,
			wantType:     EntryTask,
			wantID:       "2026-10-29-renovar-dni",
			wantTitle:    "Renovar el DNI",
			wantStatus:   "open",
			wantPriority: "high",
			wantCreated:  "2026-10-29T09:12:00+01:00",
			wantDue:      "2026-11-15T10:00:00+01:00",
			wantTags:     []string{"personal", "documentos"},
		},
		{
			name:         "reminder",
			path:         "/vault/reminders/2026-10-29-llamar-a-mama.md",
			doc:          validReminderDoc,
			wantType:     EntryReminder,
			wantID:       "2026-10-29-llamar-a-mama",
			wantTitle:    "Llamar a mamá",
			wantStatus:   "open",
			wantPriority: "normal",
			wantCreated:  "2026-10-29T09:15:00+01:00",
			wantRemindAt: "2026-10-30T19:00:00+01:00",
			wantRecur:    "weekly",
			wantTags:     []string{"familia"},
		},
		{
			name:         "event",
			path:         "/vault/events/2026-10-29-viaje-a-sevilla.md",
			doc:          validEventDoc,
			wantType:     EntryEvent,
			wantID:       "2026-10-29-viaje-a-sevilla",
			wantTitle:    "Viaje a Sevilla",
			wantStatus:   "open",
			wantPriority: "normal",
			wantCreated:  "2026-10-29T10:00:00+01:00",
			wantDue:      "2026-11-02T08:30:00+01:00",
			wantTags:     []string{"viajes"},
		},
		{
			name:         "note",
			path:         "/vault/notes/2026-10-29-lista-de-libros.md",
			doc:          validNoteDoc,
			wantType:     EntryNote,
			wantID:       "2026-10-29-lista-de-libros",
			wantTitle:    "Lista de libros",
			wantStatus:   "open",
			wantPriority: "normal",
			wantCreated:  "2026-10-29T11:00:00+01:00",
			wantTags:     nil,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			entry, problems := Parse(tc.path, []byte(tc.doc))
			if len(problems) != 0 {
				t.Fatalf("Parse(%s) = %v, want no problems", tc.path, problems)
			}
			if entry.Type != tc.wantType {
				t.Errorf("Type = %q, want %q", entry.Type, tc.wantType)
			}
			if entry.ID != tc.wantID {
				t.Errorf("ID = %q, want %q", entry.ID, tc.wantID)
			}
			if entry.Title != tc.wantTitle {
				t.Errorf("Title = %q, want %q", entry.Title, tc.wantTitle)
			}
			if entry.Status != tc.wantStatus {
				t.Errorf("Status = %q, want %q", entry.Status, tc.wantStatus)
			}
			if entry.Priority != tc.wantPriority {
				t.Errorf("Priority = %q, want %q", entry.Priority, tc.wantPriority)
			}
			if got := entry.Created.Format("2006-01-02T15:04:05Z07:00"); got != tc.wantCreated {
				t.Errorf("Created = %q, want %q", got, tc.wantCreated)
			}
			if got := formatOptional(entry.Due); got != tc.wantDue {
				t.Errorf("Due = %q, want %q", got, tc.wantDue)
			}
			if got := formatOptional(entry.RemindAt); got != tc.wantRemindAt {
				t.Errorf("RemindAt = %q, want %q", got, tc.wantRemindAt)
			}
			if entry.Recurrence != tc.wantRecur {
				t.Errorf("Recurrence = %q, want %q", entry.Recurrence, tc.wantRecur)
			}
			if strings.Join(entry.Tags, ",") != strings.Join(tc.wantTags, ",") {
				t.Errorf("Tags = %v, want %v", entry.Tags, tc.wantTags)
			}
			if !strings.Contains(entry.Body, "# ") {
				t.Errorf("Body = %q, want the markdown body kept after the frontmatter", entry.Body)
			}
		})
	}
}

func formatOptional(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.Format("2006-01-02T15:04:05Z07:00")
}

// TestParseToleratesBOMAndHorizontalRulesInBody pins two file-level cases the
// contract leaves implicit: a UTF-8 BOM is an encoding artifact and not a
// contract drift, and only the first closing "---" ends the frontmatter, so a
// body may keep its own horizontal rules and fenced blocks.
func TestParseToleratesBOMAndHorizontalRulesInBody(t *testing.T) {
	fence := "```"
	bodyWithRules := validTaskDoc + "\n---\n\n" + fence + "yaml\n---\nclave: valor\n---\n" + fence + "\n"

	tests := []struct {
		name string
		doc  string
		want string
	}{
		{name: "bom", doc: "\ufeff" + validTaskDoc, want: "# Renovar el DNI"},
		{name: "body with delimiters", doc: bodyWithRules, want: "clave: valor"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			entry, problems := Parse(validTaskPath, []byte(tc.doc))
			if len(problems) != 0 {
				t.Fatalf("Parse = %v, want no problems", problems)
			}
			if !strings.Contains(entry.Body, tc.want) {
				t.Errorf("Body = %q, want it to contain %q", entry.Body, tc.want)
			}
		})
	}
}

// wantProblem is one expected violation: the frontmatter line it must point at
// and a fragment of its message.
type wantProblem struct {
	line     int
	contains string
}

type violationCase struct {
	name string
	path string
	data string
	want []wantProblem
}

// TestParseReportsContractViolations pins each rule of the contract as its own
// case: the exact number of problems, the file every problem names, the line it
// points at and the message fragment that says what to fix.
func TestParseReportsContractViolations(t *testing.T) {
	task := taskFrontmatter
	reminder := reminderFrontmatter
	note := noteFrontmatter

	var (
		tagsAsBlockList = appendLine(replaceLine(t, task, "tags:", "tags:"), "  - personal", "  - documentos")
		tagsAsBlock     = replaceLine(t, task, "tags:", "tags: >")
		nestedMap       = appendLine(task, "meta:", "  owner: el-dueno")
		notKeyValue     = appendLine(task, "esto no es una clave")
		unknownKey      = appendLine(task, "color: azul")
		duplicateKey    = appendLine(task, "status: done")
		// A blank line inside the frontmatter, inserted right after "status:".
		blankLine       = append(append([]string(nil), task[:4]...), append([]string{""}, task[4:]...)...)
		quotedID        = replaceLine(t, task, "id:", `id: "2026-10-29-renovar-dni"`)
		upperID         = replaceLine(t, task, "id:", "id: 2026-10-29-Renovar-DNI")
		unquotedTitle   = replaceLine(t, task, "title:", "title: Renovar el DNI")
		emptyTitle      = replaceLine(t, task, "title:", `title: ""`)
		singleQuotes    = replaceLine(t, task, "title:", `title: 'Renovar el DNI'`)
		unquotedTags    = replaceLine(t, task, "tags:", "tags: personal,documentos")
		spacedTags      = replaceLine(t, task, "tags:", `tags: "personal, documentos"`)
		upperTags       = replaceLine(t, task, "tags:", `tags: "Personal"`)
		badStatus       = replaceLine(t, task, "status:", "status: pending")
		badType         = replaceLine(t, task, "type:", "type: recordatorio")
		badPriority     = replaceLine(t, task, "priority:", "priority: urgent")
		badRecurrence   = replaceLine(t, reminder, "recurrence:", "recurrence: hourly")
		zuluCreated     = replaceLine(t, task, "created:", "created: 2026-10-29T09:12:00Z")
		offsetlessDate  = replaceLine(t, task, "created:", "created: 2026-10-29T09:12:00")
		fractionalDate  = replaceLine(t, task, "due:", "due: 2026-11-15T10:00:00.123+01:00")
		dateOnly        = replaceLine(t, task, "due:", "due: 2026-11-15")
		noRemindAt      = dropLine(t, reminder, "remind_at:")
		noTaskStatus    = dropLine(t, task, "status:")
		noEventDue      = dropLine(t, eventFrontmatter, "due:")
		noNoteTags      = dropLine(t, note, "tags:")
		noNoteCreated   = dropLine(t, note, "created:")
		noteWithDue     = appendLine(note, "due: 2026-11-15T10:00:00+01:00")
		noteWithRemind  = appendLine(note, "remind_at: 2026-11-15T10:00:00+01:00")
		taskWithFired   = appendLine(task, "last_fired: 2026-11-15T10:00:00+01:00")
		taskWithRecur   = appendLine(task, "recurrence: daily")
		reminderWithDue = appendLine(reminder, "due: 2026-11-15T10:00:00+01:00")
		idOtherDate     = replaceLine(t, task, "id:", "id: 2026-10-30-renovar-dni")
	)

	cases := []violationCase{
		{
			name: "missing frontmatter",
			path: validTaskPath,
			data: "# Solo un cuerpo\n\nTexto.\n",
			want: []wantProblem{{line: 1, contains: "falta el frontmatter"}},
		},
		{
			name: "unclosed frontmatter",
			path: validTaskPath,
			data: "---\nid: 2026-10-29-renovar-dni\n",
			want: []wantProblem{{line: 1, contains: "no está cerrado"}},
		},
		{
			name: "nested mapping as block list",
			path: validTaskPath,
			data: document(tagsAsBlockList),
			want: []wantProblem{
				{line: frontmatterLine(t, tagsAsBlockList, "tags:"), contains: `falta el valor de "tags"`},
				{line: frontmatterLine(t, tagsAsBlockList, "  - personal"), contains: "lista YAML no permitida"},
				{line: frontmatterLine(t, tagsAsBlockList, "  - documentos"), contains: "lista YAML no permitida"},
			},
		},
		{
			name: "nested mapping under unknown key",
			path: validTaskPath,
			data: document(nestedMap),
			want: []wantProblem{
				{line: frontmatterLine(t, nestedMap, "meta:"), contains: `clave desconocida "meta"`},
				{line: frontmatterLine(t, nestedMap, "  owner:"), contains: "mapa anidado"},
			},
		},
		{
			name: "line that is not key value",
			path: validTaskPath,
			data: document(notKeyValue),
			want: []wantProblem{
				{line: frontmatterLine(t, notKeyValue, "esto no es"), contains: `no es "clave: valor"`},
			},
		},
		{
			name: "unknown key",
			path: validTaskPath,
			data: document(unknownKey),
			want: []wantProblem{
				{line: frontmatterLine(t, unknownKey, "color:"), contains: `clave desconocida "color"`},
			},
		},
		{
			name: "duplicate key",
			path: validTaskPath,
			data: document(duplicateKey),
			want: []wantProblem{
				{line: frontmatterLine(t, duplicateKey, "status: done"), contains: `clave duplicada "status"`},
			},
		},
		{
			name: "blank line inside frontmatter",
			path: validTaskPath,
			data: document(blankLine),
			want: []wantProblem{
				// The blank line is the one right after "status: open".
				{line: frontmatterLine(t, task, "status:") + 1, contains: "en blanco"},
			},
		},
		{
			name: "block scalar",
			path: validTaskPath,
			data: document(tagsAsBlock),
			want: []wantProblem{
				{line: frontmatterLine(t, tagsAsBlock, "tags:"), contains: "escalar plano"},
			},
		},
		{
			name: "bad type enum",
			path: validTaskPath,
			data: document(badType),
			want: []wantProblem{
				{line: frontmatterLine(t, badType, "type:"), contains: `"type" debe ser task, reminder, event o note`},
			},
		},
		{
			name: "bad status enum",
			path: validTaskPath,
			data: document(badStatus),
			want: []wantProblem{
				{line: frontmatterLine(t, badStatus, "status:"), contains: `"status" debe ser open, done o cancelled`},
			},
		},
		{
			name: "bad priority enum",
			path: validTaskPath,
			data: document(badPriority),
			want: []wantProblem{
				{line: frontmatterLine(t, badPriority, "priority:"), contains: `"priority" debe ser low, normal o high`},
			},
		},
		{
			name: "bad recurrence enum",
			path: "/vault/reminders/2026-10-29-llamar-a-mama.md",
			data: document(badRecurrence),
			want: []wantProblem{
				{line: frontmatterLine(t, badRecurrence, "recurrence:"), contains: `"recurrence" debe ser daily, weekly, monthly o yearly`},
			},
		},
		{
			name: "date with Z",
			path: validTaskPath,
			data: document(zuluCreated),
			want: []wantProblem{
				{line: frontmatterLine(t, zuluCreated, "created:"), contains: `no "Z"`},
			},
		},
		{
			name: "date without offset",
			path: validTaskPath,
			data: document(offsetlessDate),
			want: []wantProblem{
				{line: frontmatterLine(t, offsetlessDate, "created:"), contains: "RFC3339 con desfase"},
			},
		},
		{
			name: "date with fractions",
			path: validTaskPath,
			data: document(fractionalDate),
			want: []wantProblem{
				{line: frontmatterLine(t, fractionalDate, "due:"), contains: "RFC3339 con desfase"},
			},
		},
		{
			name: "date only",
			path: validTaskPath,
			data: document(dateOnly),
			want: []wantProblem{
				{line: frontmatterLine(t, dateOnly, "due:"), contains: "RFC3339 con desfase"},
			},
		},
		{
			name: "reminder without remind_at",
			path: "/vault/reminders/2026-10-29-llamar-a-mama.md",
			data: document(noRemindAt),
			want: []wantProblem{
				{line: 1, contains: "remind_at"},
			},
		},
		{
			name: "task without status",
			path: validTaskPath,
			data: document(noTaskStatus),
			want: []wantProblem{
				{line: 1, contains: `falta la clave obligatoria "status"`},
			},
		},
		{
			name: "event without due",
			path: "/vault/events/2026-10-29-viaje-a-sevilla.md",
			data: document(noEventDue),
			want: []wantProblem{
				{line: 1, contains: `falta la clave obligatoria "due"`},
			},
		},
		{
			name: "note without tags",
			path: "/vault/notes/2026-10-29-lista-de-libros.md",
			data: document(noNoteTags),
			want: []wantProblem{
				{line: 1, contains: `falta la clave obligatoria "tags"`},
			},
		},
		{
			name: "note without created",
			path: "/vault/notes/2026-10-29-lista-de-libros.md",
			data: document(noNoteCreated),
			want: []wantProblem{
				{line: 1, contains: `falta la clave obligatoria "created"`},
			},
		},
		{
			name: "due on a note",
			path: "/vault/notes/2026-10-29-lista-de-libros.md",
			data: document(noteWithDue),
			want: []wantProblem{
				{line: frontmatterLine(t, noteWithDue, "due:"), contains: `"due" no se usa`},
			},
		},
		{
			name: "remind_at on a note",
			path: "/vault/notes/2026-10-29-lista-de-libros.md",
			data: document(noteWithRemind),
			want: []wantProblem{
				{line: frontmatterLine(t, noteWithRemind, "remind_at:"), contains: `"remind_at" no se usa`},
			},
		},
		{
			name: "last_fired on a task",
			path: validTaskPath,
			data: document(taskWithFired),
			want: []wantProblem{
				{line: frontmatterLine(t, taskWithFired, "last_fired:"), contains: `"last_fired" no se usa`},
			},
		},
		{
			name: "recurrence on a task",
			path: validTaskPath,
			data: document(taskWithRecur),
			want: []wantProblem{
				{line: frontmatterLine(t, taskWithRecur, "recurrence:"), contains: `"recurrence" no se usa`},
			},
		},
		{
			name: "due on a reminder",
			path: "/vault/reminders/2026-10-29-llamar-a-mama.md",
			data: document(reminderWithDue),
			want: []wantProblem{
				{line: frontmatterLine(t, reminderWithDue, "due:"), contains: `"due" no se usa`},
			},
		},
		{
			name: "unquoted title",
			path: validTaskPath,
			data: document(unquotedTitle),
			want: []wantProblem{
				{line: frontmatterLine(t, unquotedTitle, "title:"), contains: "comillas dobles"},
			},
		},
		{
			name: "empty title",
			path: validTaskPath,
			data: document(emptyTitle),
			want: []wantProblem{
				{line: frontmatterLine(t, emptyTitle, "title:"), contains: "no puede estar vacío"},
			},
		},
		{
			name: "single quoted title",
			path: validTaskPath,
			data: document(singleQuotes),
			want: []wantProblem{
				{line: frontmatterLine(t, singleQuotes, "title:"), contains: "comillas dobles"},
			},
		},
		{
			name: "quoted id",
			path: validTaskPath,
			data: document(quotedID),
			want: []wantProblem{
				{line: frontmatterLine(t, quotedID, "id:"), contains: `"id" no se entrecomilla`},
			},
		},
		{
			name: "id with uppercase",
			path: validTaskPath,
			data: document(upperID),
			want: []wantProblem{
				{line: frontmatterLine(t, upperID, "id:"), contains: `"id" debe tener la forma`},
			},
		},
		{
			name: "id that does not match the file name",
			path: "/vault/tasks/2026-10-29-otra-cosa.md",
			data: document(task),
			want: []wantProblem{
				{line: frontmatterLine(t, task, "id:"), contains: "no coincide con id"},
			},
		},
		{
			name: "id date differs from created",
			path: "/vault/tasks/2026-10-30-renovar-dni.md",
			data: document(idOtherDate),
			want: []wantProblem{
				{line: frontmatterLine(t, idOtherDate, "id:"), contains: `no coincide con la de "created"`},
			},
		},
		{
			name: "unquoted tags",
			path: validTaskPath,
			data: document(unquotedTags),
			want: []wantProblem{
				{line: frontmatterLine(t, unquotedTags, "tags:"), contains: "comillas dobles"},
			},
		},
		{
			name: "tags with spaces",
			path: validTaskPath,
			data: document(spacedTags),
			want: []wantProblem{
				{line: frontmatterLine(t, spacedTags, "tags:"), contains: "espacios"},
			},
		},
		{
			name: "tags with uppercase",
			path: validTaskPath,
			data: document(upperTags),
			want: []wantProblem{
				{line: frontmatterLine(t, upperTags, "tags:"), contains: "minúsculas"},
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, problems := Parse(tc.path, []byte(tc.data))
			if len(problems) != len(tc.want) {
				t.Fatalf("Parse(%s) = %d problems %v, want %d", tc.path, len(problems), problems, len(tc.want))
			}
			for i, want := range tc.want {
				got := problems[i]
				if got.Path != tc.path {
					t.Errorf("problem %d names file %q, want %q", i, got.Path, tc.path)
				}
				if got.Line != want.line {
					t.Errorf("problem %d points at line %d (%v), want line %d", i, got.Line, got, want.line)
				}
				if !strings.Contains(got.Message, want.contains) {
					t.Errorf("problem %d message %q does not contain %q", i, got.Message, want.contains)
				}
			}
		})
	}
}

// TestProblemStringIsTheCLILine pins the one format the caller prints: path,
// line and message, with the line omitted only when the problem does not belong
// to a single line.
func TestProblemStringIsTheCLILine(t *testing.T) {
	if got, want := (Problem{Path: "tasks/a.md", Line: 4, Message: "algo"}).String(), "tasks/a.md:4: algo"; got != want {
		t.Errorf("Problem.String() = %q, want %q", got, want)
	}
	if got, want := (Problem{Path: "tasks/a.md", Message: "algo"}).String(), "tasks/a.md: algo"; got != want {
		t.Errorf("Problem.String() = %q, want %q", got, want)
	}
}

// TestParseFileReportsAnUnreadableFile pins the non-crashing failure mode: a
// file the process cannot read is one problem for the caller, not a panic and
// not a silently skipped entry.
func TestParseFileReportsAnUnreadableFile(t *testing.T) {
	path := "/nonexistent-vault/tasks/2026-10-29-renovar-dni.md"
	_, problems := ParseFile(path)
	if len(problems) != 1 {
		t.Fatalf("ParseFile = %d problems %v, want one", len(problems), problems)
	}
	if problems[0].Path != path {
		t.Errorf("problem names %q, want %q", problems[0].Path, path)
	}
	if !strings.Contains(problems[0].Message, "no se puede leer") {
		t.Errorf("message %q, want it to say the file cannot be read", problems[0].Message)
	}
}

// Frontmatter fixtures used as the base for the violation cases. They are the
// valid entries in the same order as the contract's key table, so a case only
// has to replace or drop the one line it is about.
var (
	taskFrontmatter = []string{
		"id: 2026-10-29-renovar-dni",
		"type: task",
		`title: "Renovar el DNI"`,
		"status: open",
		"priority: high",
		"created: 2026-10-29T09:12:00+01:00",
		"due: 2026-11-15T10:00:00+01:00",
		`tags: "personal,documentos"`,
	}

	reminderFrontmatter = []string{
		"id: 2026-10-29-llamar-a-mama",
		"type: reminder",
		`title: "Llamar a mamá"`,
		"status: open",
		"priority: normal",
		"created: 2026-10-29T09:15:00+01:00",
		"remind_at: 2026-10-30T19:00:00+01:00",
		"recurrence: weekly",
		`tags: "familia"`,
	}

	eventFrontmatter = []string{
		"id: 2026-10-29-viaje-a-sevilla",
		"type: event",
		`title: "Viaje a Sevilla"`,
		"status: open",
		"priority: normal",
		"created: 2026-10-29T10:00:00+01:00",
		"due: 2026-11-02T08:30:00+01:00",
		`tags: "viajes"`,
	}

	noteFrontmatter = []string{
		"id: 2026-10-29-lista-de-libros",
		"type: note",
		`title: "Lista de libros"`,
		"created: 2026-10-29T11:00:00+01:00",
		`tags: ""`,
	}
)

// document wraps frontmatter lines in the delimiters and a body, so the expected
// line numbers are the only thing a case has to get right.
func document(lines []string) string {
	return "---\n" + strings.Join(lines, "\n") + "\n---\n\n# Cuerpo\n\nTexto.\n"
}

// frontmatterLine returns the 1-based line of the first frontmatter line that
// starts with prefix. Line 1 is the opening "---", so index 0 is line 2.
func frontmatterLine(t *testing.T, lines []string, prefix string) int {
	t.Helper()
	for i, line := range lines {
		if strings.HasPrefix(line, prefix) {
			return i + 2
		}
	}
	t.Fatalf("fixture has no frontmatter line starting with %q: %q", prefix, lines)
	return 0
}

func replaceLine(t *testing.T, lines []string, prefix, replacement string) []string {
	t.Helper()
	out := append([]string(nil), lines...)
	for i, line := range out {
		if strings.HasPrefix(line, prefix) {
			out[i] = replacement
			return out
		}
	}
	t.Fatalf("fixture has no frontmatter line starting with %q", prefix)
	return nil
}

func dropLine(t *testing.T, lines []string, prefix string) []string {
	t.Helper()
	out := make([]string, 0, len(lines))
	dropped := false
	for _, line := range lines {
		if !dropped && strings.HasPrefix(line, prefix) {
			dropped = true
			continue
		}
		out = append(out, line)
	}
	if !dropped {
		t.Fatalf("fixture has no frontmatter line starting with %q", prefix)
	}
	return out
}

func appendLine(lines []string, extra ...string) []string {
	return append(append([]string(nil), lines...), extra...)
}
