package vault

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The tests below never touch the system clock: every instant is a literal
// fixture and every file lives in the test's own temporary directory.

// scanNow is the fixed instant the due scans use.
func scanNow() time.Time {
	return time.Date(2026, 11, 3, 12, 0, 0, 0, time.FixedZone("CET", 3600))
}

// pinnedMtime is a clearly old modification time: if a writer touches the file
// the mtime stops being this value, which is the mtime-churn check.
var pinnedMtime = time.Date(2020, 1, 2, 3, 4, 5, 0, time.UTC)

// reminderSpec describes one reminder document. Each test varies only the fields
// it is about; the rest keeps the contract's field order and valid values.
type reminderSpec struct {
	id         string // also the file name, without ".md"
	status     string
	remindAt   string
	recurrence string // empty omits the key
	lastFired  string // empty omits the key
}

// reminderDoc renders a reminder document with the frontmatter order the vault
// contract uses.
func reminderDoc(spec reminderSpec) string {
	if spec.id == "" {
		spec.id = "2026-10-29-llamar-a-mama"
	}
	if spec.status == "" {
		spec.status = "open"
	}
	if spec.remindAt == "" {
		spec.remindAt = "2026-11-03T11:00:00+01:00"
	}
	var b strings.Builder
	b.WriteString("---\n")
	fmt.Fprintf(&b, "id: %s\n", spec.id)
	b.WriteString("type: reminder\n")
	b.WriteString("title: \"Llamar a mamá\"\n")
	fmt.Fprintf(&b, "status: %s\n", spec.status)
	b.WriteString("priority: normal\n")
	fmt.Fprintf(&b, "created: %sT09:15:00+01:00\n", spec.id[:10])
	fmt.Fprintf(&b, "remind_at: %s\n", spec.remindAt)
	if spec.recurrence != "" {
		fmt.Fprintf(&b, "recurrence: %s\n", spec.recurrence)
	}
	if spec.lastFired != "" {
		fmt.Fprintf(&b, "last_fired: %s\n", spec.lastFired)
	}
	b.WriteString("tags: \"familia\"\n")
	b.WriteString("---\n\n# Llamar a mamá\n\nPreguntar por la revisión del médico.\n")
	return b.String()
}

// writeFile writes one file and fails the test if it cannot.
func writeFile(t *testing.T, path, content string) string {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("escribir %s: %v", path, err)
	}
	return path
}

// writeReminders writes the given files under <dir>/reminders and returns the
// folder path. An empty set leaves the folder absent on purpose.
func writeReminders(t *testing.T, dir string, files map[string]string) string {
	t.Helper()
	folder := filepath.Join(dir, "reminders")
	if len(files) == 0 {
		return folder
	}
	if err := os.MkdirAll(folder, 0o755); err != nil {
		t.Fatalf("crear %s: %v", folder, err)
	}
	for name, content := range files {
		writeFile(t, filepath.Join(folder, name), content)
	}
	return folder
}

// pinMtime fixes the mtime so a later comparison is deterministic even on a
// filesystem with a coarse timestamp resolution.
func pinMtime(t *testing.T, path string) {
	t.Helper()
	if err := os.Chtimes(path, pinnedMtime, pinnedMtime); err != nil {
		t.Fatalf("fijar mtime de %s: %v", path, err)
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("leer %s: %v", path, err)
	}
	return string(data)
}

func modTime(t *testing.T, path string) time.Time {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	return info.ModTime()
}

func basenames(due []Due) []string {
	names := make([]string, 0, len(due))
	for _, d := range due {
		names = append(names, filepath.Base(d.Path))
	}
	return names
}

func invalidBasenames(invalid []Invalid) []string {
	names := make([]string, 0, len(invalid))
	for _, inv := range invalid {
		names = append(names, filepath.Base(inv.Path))
	}
	return names
}

func TestScanReminders(t *testing.T) {
	const (
		past   = "2026-11-03T11:00:00+01:00"
		future = "2026-11-03T13:00:00+01:00"
	)

	cases := []struct {
		name    string
		files   map[string]string
		wantDue []string
		wantBad []string
	}{
		{
			name: "due reminder",
			files: map[string]string{
				"2026-10-29-llamar-a-mama.md": reminderDoc(reminderSpec{remindAt: past}),
			},
			wantDue: []string{"2026-10-29-llamar-a-mama.md"},
		},
		{
			name: "remind_at exactly now is already due",
			files: map[string]string{
				"2026-10-29-llamar-a-mama.md": reminderDoc(reminderSpec{remindAt: "2026-11-03T12:00:00+01:00"}),
			},
			wantDue: []string{"2026-10-29-llamar-a-mama.md"},
		},
		{
			name: "future reminder is not due",
			files: map[string]string{
				"2026-10-29-llamar-a-mama.md": reminderDoc(reminderSpec{remindAt: future}),
			},
		},
		{
			name: "already fired reminder is not due",
			files: map[string]string{
				"2026-10-29-llamar-a-mama.md": reminderDoc(reminderSpec{remindAt: past, lastFired: "2026-11-03T11:00:30+01:00"}),
			},
		},
		{
			name: "fired reminder whose remind_at moved forward is due again",
			files: map[string]string{
				"2026-10-29-llamar-a-mama.md": reminderDoc(reminderSpec{
					remindAt:  "2026-11-03T11:30:00+01:00",
					lastFired: "2026-10-27T11:30:00+01:00",
				}),
			},
			wantDue: []string{"2026-10-29-llamar-a-mama.md"},
		},
		{
			name: "cancelled reminder is not due",
			files: map[string]string{
				"2026-10-29-llamar-a-mama.md": reminderDoc(reminderSpec{remindAt: past, status: "cancelled"}),
			},
		},
		{
			name: "invalid remind_at is reported and never due",
			files: map[string]string{
				"2026-10-29-llamar-a-mama.md": reminderDoc(reminderSpec{remindAt: "2026-11-03T11:00:00"}),
			},
			wantBad: []string{"2026-10-29-llamar-a-mama.md"},
		},
		{
			name: "unclosed frontmatter is reported and never due",
			files: map[string]string{
				"2026-10-29-llamar-a-mama.md": "---\nid: 2026-10-29-llamar-a-mama\ntype: reminder\nremind_at: 2026-11-03T11:00:00+01:00\n",
			},
			wantBad: []string{"2026-10-29-llamar-a-mama.md"},
		},
		{
			name: "reminder file with a BOM is due",
			files: map[string]string{
				"2026-10-29-llamar-a-mama.md": "\uFEFF" + reminderDoc(reminderSpec{remindAt: past}),
			},
			wantDue: []string{"2026-10-29-llamar-a-mama.md"},
		},
		{
			name: "an entry of another type in reminders is reported",
			files: map[string]string{
				"2026-10-29-renovar-dni.md": "---\nid: 2026-10-29-renovar-dni\ntype: task\ntitle: \"Renovar el DNI\"\nstatus: open\ncreated: 2026-10-29T09:12:00+01:00\nremind_at: 2026-11-03T11:00:00+01:00\ntags: \"personal\"\n---\n\n# Renovar el DNI\n",
			},
			wantBad: []string{"2026-10-29-renovar-dni.md"},
		},
		{
			name: "one bad file does not stop the scan",
			files: map[string]string{
				"2026-10-29-llamar-a-mama.md": reminderDoc(reminderSpec{remindAt: past}),
				"2026-10-29-comprar-pan.md":   reminderDoc(reminderSpec{id: "2026-10-29-comprar-pan", remindAt: "ayer"}),
				"2026-10-29-regar-plantas.md": reminderDoc(reminderSpec{id: "2026-10-29-regar-plantas", remindAt: future}),
				"2026-10-29-pagar-luz.md":     reminderDoc(reminderSpec{id: "2026-10-29-pagar-luz", remindAt: past, status: "done"}),
			},
			wantDue: []string{"2026-10-29-llamar-a-mama.md"},
			wantBad: []string{"2026-10-29-comprar-pan.md"},
		},
		{
			name: "a missing reminders folder is an empty scan",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			writeReminders(t, dir, tc.files)

			report, err := ScanReminders(dir, scanNow())
			if err != nil {
				t.Fatalf("ScanReminders: %v", err)
			}
			if got := strings.Join(basenames(report.Due), ","); got != strings.Join(tc.wantDue, ",") {
				t.Errorf("due = %q, want %q", got, strings.Join(tc.wantDue, ","))
			}
			if got := strings.Join(invalidBasenames(report.Invalid), ","); got != strings.Join(tc.wantBad, ",") {
				t.Errorf("invalid = %q, want %q", got, strings.Join(tc.wantBad, ","))
			}

			// A skipped file must be reported with a reason, never silently dropped.
			for _, inv := range report.Invalid {
				if len(inv.Problems) == 0 {
					t.Errorf("invalid entry %q has no problem reported", inv.Path)
				}
			}

			due, err := DueReminders(dir, scanNow())
			if err != nil {
				t.Fatalf("DueReminders: %v", err)
			}
			if got := strings.Join(basenames(due), ","); got != strings.Join(tc.wantDue, ",") {
				t.Errorf("DueReminders = %q, want %q", got, strings.Join(tc.wantDue, ","))
			}
		})
	}
}

func TestScanRemindersDueCarriesPathEntryAndInstant(t *testing.T) {
	dir := t.TempDir()
	folder := writeReminders(t, dir, map[string]string{
		"2026-10-29-llamar-a-mama.md": reminderDoc(reminderSpec{remindAt: "2026-11-03T11:00:00+01:00"}),
	})

	report, err := ScanReminders(dir, scanNow())
	if err != nil {
		t.Fatalf("ScanReminders: %v", err)
	}
	if len(report.Due) != 1 {
		t.Fatalf("due = %d entries, want 1", len(report.Due))
	}
	due := report.Due[0]
	if want := filepath.Join(folder, "2026-10-29-llamar-a-mama.md"); due.Path != want {
		t.Errorf("path = %q, want %q", due.Path, want)
	}
	if due.Entry.ID != "2026-10-29-llamar-a-mama" {
		t.Errorf("entry id = %q, want 2026-10-29-llamar-a-mama", due.Entry.ID)
	}
	if want := "2026-11-03T11:00:00+01:00"; due.RemindAt.Format(stampLayout) != want {
		t.Errorf("remind_at = %q, want %q", due.RemindAt.Format(stampLayout), want)
	}
}

func TestMarkFiredRewritesOnlyTheLastFiredLine(t *testing.T) {
	const before = `---
id: 2026-10-29-llamar-a-mama
type: reminder
title: "Llamar a mamá"
status: open
priority: normal
created: 2026-10-29T09:15:00+01:00
remind_at: 2026-10-30T19:00:00+01:00
recurrence: weekly
last_fired: 2026-10-23T19:00:00+01:00
tags: "familia"
---

# Llamar a mamá

Preguntar por la revisión del médico.

---

Un separador en el cuerpo no es frontmatter, y la sangría tampoco se toca.
`
	// The same document with only the last_fired value replaced.
	const after = `---
id: 2026-10-29-llamar-a-mama
type: reminder
title: "Llamar a mamá"
status: open
priority: normal
created: 2026-10-29T09:15:00+01:00
remind_at: 2026-10-30T19:00:00+01:00
recurrence: weekly
last_fired: 2026-10-30T19:00:05+01:00
tags: "familia"
---

# Llamar a mamá

Preguntar por la revisión del médico.

---

Un separador en el cuerpo no es frontmatter, y la sangría tampoco se toca.
`

	at := time.Date(2026, 10, 30, 19, 0, 5, 0, time.FixedZone("CET", 3600))

	cases := []struct {
		name    string
		content string
		want    string
	}{
		{name: "plain file", content: before, want: after},
		{
			name:    "utf-8 BOM is kept",
			content: "\uFEFF" + before,
			want:    "\uFEFF" + after,
		},
		{
			name:    "CRLF line endings are kept",
			content: strings.ReplaceAll(before, "\n", "\r\n"),
			want:    strings.ReplaceAll(after, "\n", "\r\n"),
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := writeFile(t, filepath.Join(t.TempDir(), "2026-10-29-llamar-a-mama.md"), tc.content)

			if err := MarkFired(path, at); err != nil {
				t.Fatalf("MarkFired: %v", err)
			}
			if got := readFile(t, path); got != tc.want {
				t.Errorf("content after MarkFired:\n%q\nwant:\n%q", got, tc.want)
			}
			// The rewritten file must still satisfy the contract.
			if _, problems := ParseFile(path); len(problems) > 0 {
				t.Errorf("ParseFile after MarkFired: %v", problems)
			}
		})
	}
}

func TestMarkFiredWithoutLastFiredKeyFails(t *testing.T) {
	path := writeFile(t,
		filepath.Join(t.TempDir(), "2026-10-29-llamar-a-mama.md"),
		reminderDoc(reminderSpec{remindAt: "2026-11-03T11:00:00+01:00"}))
	before := readFile(t, path)

	err := MarkFired(path, scanNow())
	if err == nil {
		t.Fatal("MarkFired on a reminder without last_fired: got nil error, want a failure")
	}
	if !strings.Contains(err.Error(), "last_fired") {
		t.Errorf("error = %q, want it to name last_fired", err)
	}
	// Failing loudly means not appending a second key either.
	if got := readFile(t, path); got != before {
		t.Errorf("file changed after the failure:\n%q\nwant:\n%q", got, before)
	}
}

func TestMarkFiredIsIdempotent(t *testing.T) {
	const fired = "2026-11-03T11:00:00+01:00"
	path := writeFile(t,
		filepath.Join(t.TempDir(), "2026-10-29-llamar-a-mama.md"),
		reminderDoc(reminderSpec{remindAt: fired, lastFired: fired}))
	before := readFile(t, path)
	pinMtime(t, path)

	at := time.Date(2026, 11, 3, 11, 0, 0, 0, time.FixedZone("CET", 3600))
	if err := MarkFired(path, at); err != nil {
		t.Fatalf("MarkFired: %v", err)
	}
	if got := readFile(t, path); got != before {
		t.Errorf("content changed on an idempotent MarkFired:\n%q\nwant:\n%q", got, before)
	}
	if got := modTime(t, path); !got.Equal(pinnedMtime) {
		t.Errorf("mtime = %v, want it untouched at %v", got, pinnedMtime)
	}

	// A different instant must still write, so the no-op above is not a stub.
	if err := MarkFired(path, at.Add(time.Minute)); err != nil {
		t.Fatalf("MarkFired with a new instant: %v", err)
	}
	if got := readFile(t, path); got == before {
		t.Error("content unchanged after MarkFired with a different instant")
	}
}

func TestAdvanceRecurrence(t *testing.T) {
	cases := []struct {
		name       string
		recurrence string
		remindAt   string
		at         string
		want       string
		wantErr    bool
	}{
		{
			name:       "daily",
			recurrence: "daily",
			remindAt:   "2026-11-03T19:00:00+01:00",
			at:         "2026-11-03T19:00:00+01:00",
			want:       "2026-11-04T19:00:00+01:00",
		},
		{
			name:       "daily skips the occurrences already past",
			recurrence: "daily",
			remindAt:   "2026-11-01T19:00:00+01:00",
			at:         "2026-11-03T19:00:30+01:00",
			want:       "2026-11-04T19:00:00+01:00",
		},
		{
			name:       "weekly",
			recurrence: "weekly",
			remindAt:   "2026-10-30T19:00:00+01:00",
			at:         "2026-10-30T19:00:00+01:00",
			want:       "2026-11-06T19:00:00+01:00",
		},
		{
			name:       "monthly clamps 31 january to 28 february",
			recurrence: "monthly",
			remindAt:   "2026-01-31T08:00:00+01:00",
			at:         "2026-01-31T08:00:00+01:00",
			want:       "2026-02-28T08:00:00+01:00",
		},
		{
			name:       "monthly clamps 31 january to 29 february in a leap year",
			recurrence: "monthly",
			remindAt:   "2028-01-31T08:00:00+01:00",
			at:         "2028-01-31T08:00:00+01:00",
			want:       "2028-02-29T08:00:00+01:00",
		},
		{
			name:       "monthly returns to the 31st after a short month",
			recurrence: "monthly",
			remindAt:   "2028-01-31T08:00:00+01:00",
			at:         "2028-02-29T08:00:00+01:00",
			want:       "2028-03-31T08:00:00+02:00",
		},
		{
			name:       "yearly",
			recurrence: "yearly",
			remindAt:   "2026-11-03T19:00:00+01:00",
			at:         "2026-11-03T19:00:00+01:00",
			want:       "2027-11-03T19:00:00+01:00",
		},
		{
			name:       "yearly clamps 29 february to 28 february",
			recurrence: "yearly",
			remindAt:   "2028-02-29T09:00:00+01:00",
			at:         "2028-02-29T09:00:00+01:00",
			want:       "2029-02-28T09:00:00+01:00",
		},
		{
			name:       "the original offset is kept",
			recurrence: "daily",
			remindAt:   "2026-07-01T10:00:00+02:00",
			at:         "2026-07-01T10:00:00+02:00",
			want:       "2026-07-02T10:00:00+02:00",
		},
		{
			name:       "remind_at is already after at so nothing moves",
			recurrence: "daily",
			remindAt:   "2026-11-03T19:00:00+01:00",
			at:         "2026-11-03T12:00:00+01:00",
			want:       "2026-11-03T19:00:00+01:00",
		},
		{
			name:       "unknown recurrence",
			recurrence: "hourly",
			remindAt:   "2026-11-03T19:00:00+01:00",
			at:         "2026-11-03T19:00:00+01:00",
			wantErr:    true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := writeFile(t,
				filepath.Join(t.TempDir(), "2026-10-29-llamar-a-mama.md"),
				reminderDoc(reminderSpec{remindAt: tc.remindAt, recurrence: tc.recurrence}))
			before := readFile(t, path)

			newAt, err := AdvanceRecurrence(path, mustParse(t, tc.at))
			if tc.wantErr {
				if err == nil {
					t.Fatal("got nil error, want a failure")
				}
				if got := readFile(t, path); got != before {
					t.Errorf("file changed after the failure:\n%q\nwant:\n%q", got, before)
				}
				return
			}
			if err != nil {
				t.Fatalf("AdvanceRecurrence: %v", err)
			}
			if got := newAt.Format(stampLayout); got != tc.want {
				t.Errorf("returned instant = %q, want %q", got, tc.want)
			}

			// Only the remind_at line moves, and the result is still valid.
			wantContent := strings.Replace(before,
				"remind_at: "+tc.remindAt+"\n", "remind_at: "+tc.want+"\n", 1)
			if got := readFile(t, path); got != wantContent {
				t.Errorf("content after AdvanceRecurrence:\n%q\nwant:\n%q", got, wantContent)
			}
			entry, problems := ParseFile(path)
			if len(problems) > 0 {
				t.Fatalf("ParseFile after AdvanceRecurrence: %v", problems)
			}
			if got := entry.RemindAt.Format(stampLayout); got != tc.want {
				t.Errorf("remind_at on disk = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestAdvanceRecurrenceErrors(t *testing.T) {
	t.Run("no recurrence", func(t *testing.T) {
		path := writeFile(t,
			filepath.Join(t.TempDir(), "2026-10-29-llamar-a-mama.md"),
			reminderDoc(reminderSpec{remindAt: "2026-11-03T19:00:00+01:00"}))
		if _, err := AdvanceRecurrence(path, scanNow()); err == nil {
			t.Fatal("got nil error, want a failure")
		}
	})

	t.Run("unknown recurrence is rejected", func(t *testing.T) {
		if _, err := nextOccurrence(scanNow(), scanNow(), "hourly"); err == nil {
			t.Fatal("got nil error, want a failure")
		}
	})
}

func TestAdvanceRecurrenceIsIdempotent(t *testing.T) {
	path := writeFile(t,
		filepath.Join(t.TempDir(), "2026-10-29-llamar-a-mama.md"),
		reminderDoc(reminderSpec{remindAt: "2026-11-03T19:00:00+01:00", recurrence: "daily"}))
	at := mustParse(t, "2026-11-03T19:00:00+01:00")

	first, err := AdvanceRecurrence(path, at)
	if err != nil {
		t.Fatalf("AdvanceRecurrence: %v", err)
	}
	afterFirst := readFile(t, path)
	pinMtime(t, path)

	second, err := AdvanceRecurrence(path, at)
	if err != nil {
		t.Fatalf("second AdvanceRecurrence: %v", err)
	}
	if !second.Equal(first) {
		t.Errorf("second call returned %v, want %v", second, first)
	}
	if got := readFile(t, path); got != afterFirst {
		t.Errorf("content changed on the second call:\n%q\nwant:\n%q", got, afterFirst)
	}
	if got := modTime(t, path); !got.Equal(pinnedMtime) {
		t.Errorf("mtime = %v, want it untouched at %v", got, pinnedMtime)
	}
}

// mustParse parses a fixture instant with the contract's shape.
func mustParse(t *testing.T, value string) time.Time {
	t.Helper()
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		t.Fatalf("fixture %q: %v", value, err)
	}
	return parsed
}

// TestAdvanceRecurrenceKeepsWallClockAcrossDST pins the difference between
// "same offset" and "same hour". Europe/Madrid moves from +01:00 to +02:00 on
// 2026-03-29; a daily reminder written as 19:00 must still read 19:00, with the
// new offset, instead of drifting to 20:00 local time.
func TestAdvanceRecurrenceKeepsWallClockAcrossDST(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "reminders"), 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "reminders", "2026-03-28-revisar.md")
	body := "---\nid: 2026-03-28-revisar\ntype: reminder\ntitle: \"Revisar\"\nstatus: open\npriority: normal\ncreated: 2026-03-28T09:00:00+01:00\nremind_at: 2026-03-28T19:00:00+01:00\nrecurrence: daily\ntags: \"prueba\"\n---\n\n# Revisar\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	before, err := time.Parse("2006-01-02T15:04:05-07:00", "2026-03-28T19:00:00+01:00")
	if err != nil {
		t.Fatal(err)
	}
	got, err := AdvanceRecurrence(path, before)
	if err != nil {
		t.Fatalf("AdvanceRecurrence: %v", err)
	}

	if h, m, _ := got.Clock(); h != 19 || m != 0 {
		t.Errorf("wall clock = %02d:%02d, want 19:00 (the hour the owner wrote)", h, m)
	}
	if y, mo, d := got.Date(); y != 2026 || mo != time.March || d != 29 {
		t.Errorf("date = %s, want 2026-03-29", got.Format("2006-01-02"))
	}
	if _, offset := got.Zone(); offset != 2*3600 {
		t.Errorf("offset = %d, want +02:00 after the DST change", offset)
	}
}
