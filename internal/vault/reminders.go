package vault

// The vault side of Fase 3: which reminders are due, how to mark one as fired
// and how to move a recurring one forward. Nothing here knows about systemd. The
// markdown file is the only source of truth, so both writers touch exactly one
// frontmatter line and leave every other byte of the file identical, atomically.
// Reading reuses the contract parser in schema.go: a file that breaks the
// contract is reported and skipped, never fired.

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// stampLayout is RFC3339 with a numeric offset and no fractional seconds, the
// only shape the contract accepts. It uses "-07:00" rather than "Z07:00" so a
// UTC instant is written as "+00:00" and never as the rejected "Z".
const stampLayout = "2006-01-02T15:04:05-07:00"

// The four recurrences the contract allows.
const (
	recurrenceDaily   = "daily"
	recurrenceWeekly  = "weekly"
	recurrenceMonthly = "monthly"
	recurrenceYearly  = "yearly"
)

// Due is one reminder that has to fire.
type Due struct {
	// Path is the reminder file.
	Path string
	// Entry is its parsed frontmatter.
	Entry Entry
	// RemindAt is the entry's remind_at, at or before the scan time.
	RemindAt time.Time
}

// Invalid is one reminder file that was skipped because it does not satisfy the
// vault contract. A skipped file is never fired.
type Invalid struct {
	// Path is the reminder file.
	Path string
	// Problems lists every violation found in it.
	Problems []Problem
}

// DueReport is the result of one scan of the reminders folder.
type DueReport struct {
	// Due holds the reminders that have to fire, in file-name order.
	Due []Due
	// Invalid holds the files that were skipped, in file-name order.
	Invalid []Invalid
}

// ScanReminders walks <dir>/reminders and splits it into the entries that are
// due at now and the entries that cannot be used. A missing reminders folder is
// an empty report, because the vault is created lazily; an unreadable one is an
// error. It never aborts the whole scan because of one bad file.
func ScanReminders(dir string, now time.Time) (DueReport, error) {
	var report DueReport
	folder := filepath.Join(dir, "reminders")
	items, err := os.ReadDir(folder)
	if err != nil {
		if os.IsNotExist(err) {
			return report, nil
		}
		return report, fmt.Errorf("no se puede leer la carpeta %q: %w", folder, err)
	}

	for _, item := range items {
		name := item.Name()
		if item.IsDir() || !item.Type().IsRegular() || !strings.HasSuffix(name, ".md") {
			continue
		}
		path := filepath.Join(folder, name)
		entry, problems := ParseFile(path)
		if len(problems) > 0 {
			report.Invalid = append(report.Invalid, Invalid{Path: path, Problems: problems})
			continue
		}
		// The folder decides the type, and only a reminder can be fired.
		if entry.Type != EntryReminder {
			report.Invalid = append(report.Invalid, Invalid{Path: path, Problems: []Problem{{
				Path:    path,
				Line:    entry.Line("type"),
				Message: fmt.Sprintf("type %q no corresponde a la carpeta %q", entry.Type, "reminders"),
			}}})
			continue
		}
		if !DueAt(entry, now) {
			continue
		}
		report.Due = append(report.Due, Due{Path: path, Entry: entry, RemindAt: entry.RemindAt})
	}
	return report, nil
}

// DueReminders returns only the reminders that are due at now. It is the narrow
// form of ScanReminders for callers that do not print the skipped files; use
// ScanReminders when the problems matter.
func DueReminders(dir string, now time.Time) ([]Due, error) {
	report, err := ScanReminders(dir, now)
	if err != nil {
		return nil, err
	}
	return report.Due, nil
}

// DueAt reports whether one parsed entry has to fire at now. It is due when it
// is an open reminder whose remind_at has arrived, and when it has not fired yet
// or its remind_at was moved forward after the last firing, so a recurring or
// rescheduled reminder becomes due again without a second flag. Two details the
// plan leaves implicit live here: the firing time is compared to remind_at, not
// to last_fired (a reminder fired late is still the reminder of that
// remind_at), and remind_at exactly equal to now counts as due.
func DueAt(entry Entry, now time.Time) bool {
	if entry.Type != EntryReminder || entry.Status != "open" {
		return false
	}
	if entry.RemindAt.IsZero() || entry.RemindAt.After(now) {
		return false
	}
	if entry.LastFired.IsZero() {
		return true
	}
	return entry.LastFired.Before(entry.RemindAt)
}

// MarkFired records that the reminder at path fired at at, rewriting only the
// last_fired line. The frontmatter must already carry that key: appending it
// would change the contract's field order and could hide a reminder created
// without it, so a missing key is an error. Writing the same instant twice does
// not touch the file.
func MarkFired(path string, at time.Time) error {
	return rewriteFrontmatterValue(path, "last_fired", formatStamp(at))
}

// AdvanceRecurrence moves a recurring reminder's remind_at to its next
// occurrence strictly after at and returns the new instant. The next occurrence
// is counted from the current remind_at, so a reminder fired late skips only the
// occurrences that are already past. Calling it twice with the same at is safe:
// the second call finds remind_at already after at and writes nothing.
func AdvanceRecurrence(path string, at time.Time) (time.Time, error) {
	entry, problems := ParseFile(path)
	if len(problems) > 0 {
		return time.Time{}, fmt.Errorf("%s: %s", path, problems[0].Message)
	}
	if entry.Recurrence == "" {
		return time.Time{}, fmt.Errorf("%s: el recordatorio no tiene recurrence: no hay nada que adelantar", path)
	}
	if entry.RemindAt.IsZero() {
		return time.Time{}, fmt.Errorf("%s: el recordatorio no tiene un remind_at válido", path)
	}
	next, err := nextOccurrence(entry.RemindAt, at, entry.Recurrence)
	if err != nil {
		return time.Time{}, fmt.Errorf("%s: %w", path, err)
	}
	if err := rewriteFrontmatterValue(path, "remind_at", formatStamp(next)); err != nil {
		return time.Time{}, err
	}
	return next, nil
}

// nextOccurrence returns the first occurrence of recurrence strictly after at,
// counting from remindAt. An unknown recurrence is an error, never a silent
// no-op.
func nextOccurrence(remindAt, at time.Time, recurrence string) (time.Time, error) {
	switch recurrence {
	case recurrenceDaily, recurrenceWeekly, recurrenceMonthly, recurrenceYearly:
	default:
		return time.Time{}, fmt.Errorf("recurrence desconocida %q: debe ser %s, %s, %s o %s",
			recurrence, recurrenceDaily, recurrenceWeekly, recurrenceMonthly, recurrenceYearly)
	}
	// Each step is counted from the original remind_at instead of the previous
	// step, so a clamped month does not drag the day of month down for good:
	// 31 Jan, 28 Feb, 31 Mar, not 31 Jan, 28 Feb, 28 Mar. Step 0 is remind_at
	// itself, which is already the next occurrence when it is still ahead of at;
	// that is what makes a second call with the same at a no-op.
	for steps := 0; ; steps++ {
		candidate := occurrence(remindAt, recurrence, steps)
		if candidate.After(at) {
			return candidate, nil
		}
	}
}

// occurrence returns the n-th occurrence of recurrence after remindAt. The day
// and the time of day are those of remindAt in the host zone, clamped to the
// length of the target month. The offset is resolved for the resulting instant,
// so a reminder keeps the wall-clock hour the owner wrote even when a DST change
// moves the numeric offset.
func occurrence(remindAt time.Time, recurrence string, steps int) time.Time {
	// Wall-clock stability, not offset stability: a weekly reminder written as
	// 19:00 in winter must still be 19:00 after the DST change, even though the
	// numeric offset moves from +01:00 to +02:00. Doing the arithmetic in the
	// host zone and letting Go resolve the offset for that instant is what keeps
	// the hour the owner wrote.
	loc := time.Local
	local := remindAt.In(loc)
	year, month, day := local.Date()
	hour, minute, second := local.Clock()
	nsec := local.Nanosecond()

	switch recurrence {
	case recurrenceDaily:
		return time.Date(year, month, day+steps, hour, minute, second, nsec, loc)
	case recurrenceWeekly:
		return time.Date(year, month, day+7*steps, hour, minute, second, nsec, loc)
	case recurrenceMonthly:
		months := int(month) - 1 + steps
		target := time.Month(months%12 + 1)
		targetYear := year + months/12
		return time.Date(targetYear, target, clampDay(targetYear, target, day), hour, minute, second, nsec, loc)
	default: // recurrenceYearly, the only one left after nextOccurrence's check
		targetYear := year + steps
		return time.Date(targetYear, month, clampDay(targetYear, month, day), hour, minute, second, nsec, loc)
	}
}

// daysIn returns the length of one month in the proleptic Gregorian calendar.
// Day 0 of the next month is the last day of this one.
func daysIn(year int, month time.Month) int {
	return time.Date(year, month+1, 0, 12, 0, 0, 0, time.UTC).Day()
}

// clampDay keeps day inside the month, so 31 January plus one month is the last
// day of February.
func clampDay(year int, month time.Month, day int) int {
	if last := daysIn(year, month); day > last {
		return last
	}
	return day
}

// formatStamp renders an instant in the contract's only accepted shape.
func formatStamp(t time.Time) string { return t.Format(stampLayout) }

// rewriteFrontmatterValue replaces the value of one frontmatter key and writes
// the file atomically. Every other byte stays identical, including a UTF-8 BOM,
// the line endings and the body. When the resulting file would be byte for byte
// the one on disk, it does not write at all, so calling a writer twice never
// churns the mtime.
func rewriteFrontmatterValue(path, key, value string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("no se puede leer %s: %w", path, err)
	}
	// The parser owns the frontmatter: it finds the key's line and refuses
	// duplicates, so the line number it reports is the one to replace.
	entry, _ := Parse(path, data)
	line := entry.Line(key)
	if line == 0 {
		return fmt.Errorf("%s: el frontmatter no tiene la clave %q y no se añade: escribe la entrada con el contrato del vault", path, key)
	}
	lines := byteLines(data)
	if line > len(lines) {
		return fmt.Errorf("%s: no se puede localizar la línea %d de %q", path, line, key)
	}
	updated := replaceFrontmatterLine(lines, line-1, key+": "+value)
	if bytes.Equal(updated, data) {
		return nil
	}
	return writeAtomic(path, updated)
}

// byteLines splits data on "\n" and keeps every terminator, so joining the
// result reproduces data byte for byte. Lines hold the same index Parse reports,
// which is what lets the two work together.
func byteLines(data []byte) [][]byte {
	var lines [][]byte
	for len(data) > 0 {
		if i := bytes.IndexByte(data, '\n'); i >= 0 {
			lines = append(lines, data[:i+1])
			data = data[i+1:]
			continue
		}
		lines = append(lines, data)
		break
	}
	return lines
}

// replaceFrontmatterLine returns the joined file with one line replaced, keeping
// that line's own terminator so a CRLF file stays CRLF.
func replaceFrontmatterLine(lines [][]byte, index int, text string) []byte {
	var buf bytes.Buffer
	for i, line := range lines {
		if i != index {
			buf.Write(line)
			continue
		}
		buf.WriteString(text)
		if bytes.HasSuffix(line, []byte("\r\n")) {
			buf.WriteString("\r\n")
		} else if bytes.HasSuffix(line, []byte("\n")) {
			buf.WriteString("\n")
		}
	}
	return buf.Bytes()
}

// writeAtomic writes data to a temporary file in the target's own directory and
// renames it over the original, so a crash never leaves a half-written entry and
// a reader never sees a mix of old and new bytes. The original permissions are
// kept and the temporary file never survives a failure.
func writeAtomic(path string, data []byte) error {
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("no se puede leer %s: %w", path, err)
	}
	// Temporary files are not entries and must not look like one: no ".md".
	f, err := os.CreateTemp(filepath.Dir(path), ".reminder-*")
	if err != nil {
		return fmt.Errorf("no se puede escribir junto a %s: %w", path, err)
	}
	tmp := f.Name()
	discard := func() {
		_ = f.Close()
		_ = os.Remove(tmp)
	}
	if _, err := f.Write(data); err != nil {
		discard()
		return fmt.Errorf("no se puede escribir %s: %w", path, err)
	}
	if err := f.Sync(); err != nil {
		discard()
		return fmt.Errorf("no se puede escribir %s: %w", path, err)
	}
	if err := f.Chmod(info.Mode().Perm()); err != nil {
		discard()
		return fmt.Errorf("no se pueden conservar los permisos de %s: %w", path, err)
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("no se puede escribir %s: %w", path, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("no se puede reemplazar %s: %w", path, err)
	}
	return nil
}
