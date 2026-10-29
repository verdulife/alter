package vault

// The vault contract lives in .agents/skills/alter-vault/SKILL.md: one markdown
// file per entry, a flat YAML frontmatter between two "---" lines and a free
// markdown body. Pi writes those files with write/edit, so nothing enforces the
// contract at write time. This file is the enforcement: a flat frontmatter parser
// and a validator that returns every violation with the path and line the caller
// has to fix, without reading or writing anything outside the given files.

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// EntryType is the kind of vault entry. It decides which keys are required,
// optional or forbidden.
type EntryType string

// The four entry types, one folder each.
const (
	EntryTask     EntryType = "task"
	EntryReminder EntryType = "reminder"
	EntryEvent    EntryType = "event"
	EntryNote     EntryType = "note"
)

// requiredKeys and optionalKeys are the contract's obligation matrix per type.
// Any key in neither list is forbidden for that type.
var (
	requiredKeys = map[EntryType][]string{
		EntryTask:     {"id", "type", "title", "status", "created", "tags"},
		EntryReminder: {"id", "type", "title", "status", "created", "remind_at", "tags"},
		EntryEvent:    {"id", "type", "title", "status", "created", "due", "tags"},
		EntryNote:     {"id", "type", "title", "created", "tags"},
	}
	optionalKeys = map[EntryType][]string{
		EntryTask:     {"priority", "due", "remind_at"},
		EntryReminder: {"priority", "recurrence", "last_fired"},
		EntryEvent:    {"priority", "remind_at", "recurrence"},
		EntryNote:     {"status", "priority"},
	}
	// universalRequired are the keys every type requires, used when "type" itself
	// is missing or invalid and the per-type matrix cannot be applied.
	universalRequired = []string{"id", "title", "created", "tags"}
)

// knownKeys is the closed set of frontmatter keys. Anything else means the writer
// drifted from the contract.
var knownKeys = map[string]bool{
	"id": true, "type": true, "title": true, "status": true, "priority": true,
	"created": true, "due": true, "remind_at": true, "recurrence": true,
	"last_fired": true, "tags": true,
}

var (
	// reID is <YYYY-MM-DD>-<slug>, with the contract's slug alphabet.
	reID = regexp.MustCompile(`^(\d{4}-\d{2}-\d{2})-([a-z0-9]+(-[a-z0-9]+)*)$`)
	// reDateTime is RFC3339 with a numeric offset, without "Z" and without
	// fractional seconds: the exact shape the contract shows.
	reDateTime = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}[+-]\d{2}:\d{2}$`)
	// reTag is the safe shape of one inline tag: lowercase, digits, dash and
	// underscore.
	reTag = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]*$`)
)

// dateTimeExample is the exact value shape the contract requires.
const dateTimeExample = "2026-10-29T18:30:00+01:00"

// Problem is one contract violation. Path is the file it belongs to and Line the
// 1-based frontmatter line; a problem about the frontmatter block as a whole (a
// missing required key, an absent frontmatter) points at line 1. Line 0 means
// no line is known, which only happens for a file that could not be read.
type Problem struct {
	Path    string
	Line    int
	Message string
}

// String renders the one-line form the command prints: path:line: message.
func (p Problem) String() string {
	switch {
	case p.Line > 0:
		return fmt.Sprintf("%s:%d: %s", p.Path, p.Line, p.Message)
	case p.Path != "":
		return fmt.Sprintf("%s: %s", p.Path, p.Message)
	default:
		return p.Message
	}
}

// Entry is a parsed vault entry. Times are zero when the key is absent.
type Entry struct {
	ID         string
	Type       EntryType
	Title      string
	Status     string
	Priority   string
	Created    time.Time
	Due        time.Time
	RemindAt   time.Time
	Recurrence string
	LastFired  time.Time
	Tags       []string
	Body       string

	lines map[string]int
}

// Line returns the 1-based line of a frontmatter key, or 0 when the key is
// absent.
func (e Entry) Line(key string) int { return e.lines[key] }

// field is one parsed key: its line and its raw, still unquoted value.
type field struct {
	line int
	raw  string
}

type parser struct {
	path     string
	fields   map[string]field
	problems []Problem
}

func (p *parser) add(line int, format string, args ...any) {
	p.problems = append(p.problems, Problem{
		Path:    p.path,
		Line:    line,
		Message: fmt.Sprintf(format, args...),
	})
}

// done returns the problems ordered by line, so the output of one file reads top
// to bottom.
func (p *parser) done() []Problem {
	sort.SliceStable(p.problems, func(i, j int) bool { return p.problems[i].Line < p.problems[j].Line })
	return p.problems
}

// sortedKeys returns the present keys ordered by their line.
func (p *parser) sortedKeys() []string {
	keys := make([]string, 0, len(p.fields))
	for key := range p.fields {
		keys = append(keys, key)
	}
	sort.SliceStable(keys, func(i, j int) bool { return p.fields[keys[i]].line < p.fields[keys[j]].line })
	return keys
}

// Parse splits a vault entry into frontmatter and body, then validates both the
// shape of the frontmatter and the rules of its type. It never fails: everything
// it cannot accept becomes a Problem.
func Parse(path string, data []byte) (Entry, []Problem) {
	p := &parser{path: path, fields: make(map[string]field)}
	lines := splitLines(data)

	if len(lines) == 0 || lines[0] != "---" {
		p.add(1, `falta el frontmatter: la línea 1 debe ser "---"`)
		return Entry{}, p.done()
	}
	closeAt := -1
	for i := 1; i < len(lines); i++ {
		if lines[i] == "---" {
			closeAt = i
			break
		}
	}
	if closeAt < 0 {
		p.add(1, `el frontmatter no está cerrado: falta una línea "---"`)
		return Entry{}, p.done()
	}
	for i := 1; i < closeAt; i++ {
		p.parseLine(i+1, lines[i])
	}

	entry := p.validate()
	// Only the first closing delimiter ends the frontmatter, so a body keeps its
	// own horizontal rules and fenced blocks.
	entry.Body = strings.Trim(strings.Join(lines[closeAt+1:], "\n"), "\n")
	return entry, p.done()
}

// ParseFile reads and validates one entry. An unreadable file is one problem
// without a line, not a crash and not a silently skipped entry.
func ParseFile(path string) (Entry, []Problem) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Entry{}, []Problem{{Path: path, Message: fmt.Sprintf("no se puede leer el fichero: %v", err)}}
	}
	return Parse(path, data)
}

// splitLines turns the file bytes into lines with line numbers implied by index.
// A UTF-8 BOM is an encoding artifact, so it is dropped; a CRLF file keeps the
// same line numbers because the CR is dropped too.
func splitLines(data []byte) []string {
	text := strings.TrimPrefix(string(data), "\uFEFF")
	if text == "" {
		return nil
	}
	lines := strings.Split(text, "\n")
	for i, line := range lines {
		lines[i] = strings.TrimSuffix(line, "\r")
	}
	return lines
}

// parseLine accepts exactly one flat "key: value" line. Indentation, block
// lists, block scalars, unknown keys, duplicates and missing values are all
// reported with the line they appear on.
func (p *parser) parseLine(n int, line string) {
	trimmed := strings.TrimSpace(line)
	if trimmed == "" {
		p.add(n, "línea en blanco dentro del frontmatter")
		return
	}
	if line != trimmed {
		if trimmed == "-" || strings.HasPrefix(trimmed, "- ") {
			p.add(n, `lista YAML no permitida: usa una lista en línea (tags: "a,b")`)
			return
		}
		p.add(n, "sangría o mapa anidado no permitido: el frontmatter es plano")
		return
	}

	key, value, ok := strings.Cut(line, ":")
	if !ok {
		p.add(n, `no es "clave: valor"`)
		return
	}
	key = strings.TrimSpace(key)
	value = strings.TrimSpace(value)
	if !knownKeys[key] {
		p.add(n, "clave desconocida %q: el frontmatter solo admite las claves del contrato", key)
		return
	}
	if _, duplicate := p.fields[key]; duplicate {
		p.add(n, "clave duplicada %q: una entrada no repite una clave", key)
		return
	}
	if value == "" {
		p.add(n, "falta el valor de %q", key)
		// The key counts as present so the missing-required check does not report
		// the same absence twice.
		p.fields[key] = field{line: n}
		return
	}
	p.fields[key] = field{line: n, raw: value}
}

// validate checks every value and then the per-type obligations, and returns the
// entry with the fields that were valid.
func (p *parser) validate() Entry {
	entry := Entry{lines: make(map[string]int, len(p.fields))}
	for key, f := range p.fields {
		entry.lines[key] = f.line
	}
	for _, key := range p.sortedKeys() {
		p.validateValue(&entry, key, p.fields[key])
	}

	if id, ok := p.fields["id"]; ok && entry.ID != "" && !entry.Created.IsZero() {
		if entry.Created.Format("2006-01-02") != entry.ID[:10] {
			p.add(id.line, `la fecha de "id" no coincide con la de "created"`)
		}
	}
	// The contract ties id and file name together: the name is the id plus .md.
	if entry.ID != "" && strings.HasSuffix(p.path, ".md") {
		if base := filepath.Base(p.path); base != entry.ID+".md" {
			p.add(p.fields["id"].line, "el nombre del fichero %q no coincide con id %q", base, entry.ID)
		}
	}

	required, knownType := requiredKeys[entry.Type]
	if !knownType {
		// "type" is missing or invalid (already reported), so the per-type matrix
		// cannot say anything more than the keys every type requires.
		for _, key := range universalRequired {
			if _, present := p.fields[key]; !present {
				p.add(1, "falta la clave obligatoria %q", key)
			}
		}
		return entry
	}

	for _, key := range required {
		if _, present := p.fields[key]; present {
			continue
		}
		if entry.Type == EntryReminder && key == "remind_at" {
			p.add(1, `falta la clave obligatoria "remind_at" para type=reminder: un recordatorio sin remind_at es inválido`)
			continue
		}
		p.add(1, "falta la clave obligatoria %q para type=%s", key, entry.Type)
	}

	allowed := make(map[string]bool, len(required)+len(optionalKeys[entry.Type])+1)
	for _, key := range required {
		allowed[key] = true
	}
	for _, key := range optionalKeys[entry.Type] {
		allowed[key] = true
	}
	for _, key := range p.sortedKeys() {
		if !allowed[key] {
			p.add(p.fields[key].line, "%q no se usa en una entrada de tipo %s", key, entry.Type)
		}
	}

	// Documented defaults, applied only when the key is absent so an invalid value
	// is never silently replaced.
	if _, present := p.fields["status"]; !present {
		entry.Status = "open"
	}
	if _, present := p.fields["priority"]; !present {
		entry.Priority = "normal"
	}
	return entry
}

// validateValue checks one key's value and copies the typed value into the entry.
func (p *parser) validateValue(entry *Entry, key string, f field) {
	value := f.raw
	if value == "" {
		return // already reported by parseLine
	}
	if strings.ContainsAny(value[:1], "|>[{&*!%@`#") {
		p.add(f.line, "el valor de %q debe ser un escalar plano (sin listas, mapas, bloques ni anclas)", key)
		return
	}
	if key != "title" && key != "tags" && strings.HasPrefix(value, `"`) {
		p.add(f.line, "%q no se entrecomilla: el contrato lo escribe sin comillas", key)
		return
	}

	switch key {
	case "id":
		match := reID.FindStringSubmatch(value)
		if match == nil {
			p.add(f.line, `"id" debe tener la forma <YYYY-MM-DD>-<slug> (minúsculas, números y guiones)`)
			return
		}
		if len(match[2]) > 60 {
			p.add(f.line, "el slug de %q supera los 60 caracteres", value)
			return
		}
		entry.ID = value

	case "type":
		switch EntryType(value) {
		case EntryTask, EntryReminder, EntryEvent, EntryNote:
			entry.Type = EntryType(value)
		default:
			p.add(f.line, `"type" debe ser task, reminder, event o note (es %q)`, value)
		}

	case "title":
		inner, ok := unquote(value)
		if !ok {
			p.add(f.line, `"title" debe ir entre comillas dobles: title: "texto"`)
			return
		}
		if strings.TrimSpace(inner) == "" {
			p.add(f.line, `"title" no puede estar vacío`)
			return
		}
		entry.Title = inner

	case "status":
		switch value {
		case "open", "done", "cancelled":
			entry.Status = value
		default:
			p.add(f.line, `"status" debe ser open, done o cancelled (es %q)`, value)
		}

	case "priority":
		switch value {
		case "low", "normal", "high":
			entry.Priority = value
		default:
			p.add(f.line, `"priority" debe ser low, normal o high (es %q)`, value)
		}

	case "recurrence":
		switch value {
		case "daily", "weekly", "monthly", "yearly":
			entry.Recurrence = value
		default:
			p.add(f.line, `"recurrence" debe ser daily, weekly, monthly o yearly (es %q)`, value)
		}

	case "created":
		entry.Created = p.dateTime(key, f)

	case "due":
		entry.Due = p.dateTime(key, f)

	case "remind_at":
		entry.RemindAt = p.dateTime(key, f)

	case "last_fired":
		entry.LastFired = p.dateTime(key, f)

	case "tags":
		inner, ok := unquote(value)
		if !ok {
			p.add(f.line, `"tags" debe ir entre comillas dobles, con la lista separada por comas (tags: "personal,dni")`)
			return
		}
		if inner == "" {
			return // tags: "" is the documented empty list
		}
		tags := make([]string, 0, 4)
		valid := true
		for _, tag := range strings.Split(inner, ",") {
			switch {
			case tag == "":
				p.add(f.line, `"tags" tiene una etiqueta vacía: quita la coma sobrante`)
				valid = false
			case strings.ContainsAny(tag, " \t"):
				p.add(f.line, `"tags" no admite espacios: se escribe tags: "personal,dni"`)
				valid = false
			case tag != strings.ToLower(tag) || !reTag.MatchString(tag):
				p.add(f.line, "la etiqueta %q debe estar en minúsculas y usar solo letras, números, guiones o guiones bajos", tag)
				valid = false
			default:
				tags = append(tags, tag)
			}
		}
		if valid {
			entry.Tags = tags
		}
	}
}

// dateTime parses one of the four date keys, rejecting "Z", a missing offset, a
// date-only value and fractional seconds with one message that shows the shape.
func (p *parser) dateTime(key string, f field) time.Time {
	value := f.raw
	if strings.HasSuffix(value, "Z") {
		p.add(f.line, "%q debe ser RFC3339 con desfase numérico (%s), no %q", key, dateTimeExample, "Z")
		return time.Time{}
	}
	if !reDateTime.MatchString(value) {
		p.add(f.line, "%q no es una fecha RFC3339 con desfase: se espera %s (es %q)", key, dateTimeExample, value)
		return time.Time{}
	}
	ts, err := time.Parse(time.RFC3339, value)
	if err != nil {
		p.add(f.line, "%q no es una fecha RFC3339 con desfase: se espera %s (es %q)", key, dateTimeExample, value)
		return time.Time{}
	}
	return ts
}

// unquote unwraps a double-quoted scalar, accepting only the contract's one
// escape (\" for an internal quote). It reports false when the value is not
// double-quoted or when it contains a raw internal quote.
func unquote(value string) (string, bool) {
	if len(value) < 2 || !strings.HasPrefix(value, `"`) || !strings.HasSuffix(value, `"`) {
		return "", false
	}
	inner := value[1 : len(value)-1]
	var b strings.Builder
	escaped := false
	for i := 0; i < len(inner); i++ {
		c := inner[i]
		if escaped {
			if c != '"' && c != '\\' {
				return "", false
			}
			b.WriteByte(c)
			escaped = false
			continue
		}
		switch c {
		case '\\':
			escaped = true
		case '"':
			return "", false
		default:
			b.WriteByte(c)
		}
	}
	if escaped {
		return "", false
	}
	return b.String(), true
}

// CheckReport is the result of validating one vault tree.
type CheckReport struct {
	// Entries is the number of markdown entries found in the four entry folders.
	Entries int
	// Problems holds every violation, in folder and file-name order.
	Problems []Problem
}

// entryFolders is the contract's folder-to-type mapping. A folder that does not
// exist is not an error: the vault is created lazily.
var entryFolders = []struct {
	name string
	typ  EntryType
}{
	{"tasks", EntryTask},
	{"reminders", EntryReminder},
	{"events", EntryEvent},
	{"notes", EntryNote},
}

// CheckVault validates every markdown entry under root. Only the four entry
// folders are inspected: everything else in the vault (inbox/, loose files) is
// not an entry. The root itself must exist, because reporting a clean vault for
// a mistyped path would hide the mistake.
func CheckVault(root string) (CheckReport, error) {
	if _, err := os.Stat(root); err != nil {
		return CheckReport{}, fmt.Errorf("no se puede leer el vault %q: %w", root, err)
	}

	var report CheckReport
	for _, folder := range entryFolders {
		dir := filepath.Join(root, folder.name)
		items, err := os.ReadDir(dir)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return report, fmt.Errorf("no se puede leer la carpeta %q: %w", dir, err)
		}
		for _, item := range items {
			name := item.Name()
			if item.IsDir() || !item.Type().IsRegular() || !strings.HasSuffix(name, ".md") {
				continue
			}
			path := filepath.Join(dir, name)
			entry, problems := ParseFile(path)
			report.Entries++
			report.Problems = append(report.Problems, problems...)
			// The folder decides the type; an entry filed elsewhere is misplaced even
			// when its own frontmatter is valid.
			if entry.Type != "" && entry.Type != folder.typ {
				report.Problems = append(report.Problems, Problem{
					Path:    path,
					Line:    entry.Line("type"),
					Message: fmt.Sprintf("type %q no corresponde a la carpeta %q", entry.Type, folder.name),
				})
			}
		}
	}
	return report, nil
}
