package vault

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func fixedNow() time.Time {
	return time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)
}

func newTestStore(t *testing.T, maxBytes int64) *Store {
	t.Helper()
	s := New(t.TempDir(), maxBytes)
	s.now = fixedNow
	return s
}

func TestSanitizeName(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"notas.md", "notas.md"},
		{"../../etc/passwd", "passwd"},
		{`C:\users\a\doc.pdf`, "doc.pdf"},
		{"  ..hidden  ", "hidden"},
		{"con\ttab.txt", "contab.txt"},
		{"", "file"},
		{"...", "file"},
	} {
		if got := SanitizeName(tc.in); got != tc.want {
			t.Errorf("SanitizeName(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestSaveInboundKeepsFilesInsideInbox(t *testing.T) {
	s := newTestStore(t, 0)

	path, err := s.SaveInbound("../../escape.txt", strings.NewReader("hola"))
	if err != nil {
		t.Fatalf("SaveInbound: %v", err)
	}
	want := filepath.Join(s.Root(), "inbox", "2026-10-28-escape.txt")
	if path != want {
		t.Errorf("path = %q, want %q", path, want)
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "hola" {
		t.Errorf("content = %q (err %v), want hola", data, err)
	}
}

func TestSaveInboundNeverOverwrites(t *testing.T) {
	s := newTestStore(t, 0)

	first, err := s.SaveInbound("a.txt", strings.NewReader("uno"))
	if err != nil {
		t.Fatalf("SaveInbound: %v", err)
	}
	second, err := s.SaveInbound("a.txt", strings.NewReader("dos"))
	if err != nil {
		t.Fatalf("SaveInbound: %v", err)
	}

	if first == second {
		t.Fatalf("both writes landed on %q, want distinct paths", first)
	}
	if !strings.HasSuffix(second, "-1.txt") {
		t.Errorf("second = %q, want a -1 suffix", second)
	}
	if data, _ := os.ReadFile(first); string(data) != "uno" {
		t.Errorf("first content = %q, want uno (must not be overwritten)", data)
	}
}

func TestSaveInboundRejectsOversizedFile(t *testing.T) {
	s := newTestStore(t, 4)

	// Exactly at the limit is accepted.
	if _, err := s.SaveInbound("ok.txt", strings.NewReader("1234")); err != nil {
		t.Fatalf("SaveInbound(at limit): %v", err)
	}
	// One byte over is refused, and nothing is left behind.
	if _, err := s.SaveInbound("big.txt", strings.NewReader("12345")); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("SaveInbound(over limit) = %v, want ErrTooLarge", err)
	}
	entries, _ := os.ReadDir(filepath.Join(s.Root(), "inbox"))
	if len(entries) != 1 {
		t.Errorf("inbox entries = %d, want 1 (rejected file must not persist)", len(entries))
	}
}

func TestSaveNoteWritesVaultFrontmatter(t *testing.T) {
	s := newTestStore(t, 0)

	path, err := s.SaveNote(Note{
		Folder:      "alter",
		Title:       "Ideas de octubre",
		Description: "Notas del proyecto alter.",
		Body:        "Contenido de la nota.",
		Tags:        []string{"alter", "ideas"},
	})
	if err != nil {
		t.Fatalf("SaveNote: %v", err)
	}
	if want := filepath.Join(s.Root(), "alter", "ideas-de-octubre.md"); path != want {
		t.Errorf("path = %q, want %q", path, want)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read note: %v", err)
	}
	content := string(data)
	for _, want := range []string{
		"---\n",
		"title: Ideas de octubre\n",
		"description: Notas del proyecto alter.\n",
		"tags:\n  - alter\n  - ideas\n",
		"date: 2026-10-28\n",
		"# Ideas de octubre\n",
		"Contenido de la nota.",
	} {
		if !strings.Contains(content, want) {
			t.Errorf("note missing %q in:\n%s", want, content)
		}
	}
	if !strings.HasPrefix(content, "---\n") {
		t.Error("note must start with the frontmatter delimiter")
	}
}

func TestSaveNoteRejectsEscapingFolder(t *testing.T) {
	s := newTestStore(t, 0)
	if _, err := s.SaveNote(Note{Folder: "../../tmp", Title: "x", Body: "y"}); err == nil {
		t.Error("SaveNote(escaping folder) = nil error, want rejection")
	}
}

func TestAppendLineKeepsContentAndAddsSeparator(t *testing.T) {
	s := newTestStore(t, 0)
	path, err := s.SaveNote(Note{Title: "IA utiles", Body: "intro"})
	if err != nil {
		t.Fatalf("SaveNote: %v", err)
	}

	rel, err := filepath.Rel(s.Root(), path)
	if err != nil {
		t.Fatalf("rel: %v", err)
	}
	if err := s.AppendLine(rel, "- [título](https://t.me) — canal x"); err != nil {
		t.Fatalf("AppendLine: %v", err)
	}

	data, _ := os.ReadFile(path)
	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	last := lines[len(lines)-1]
	if last != "- [título](https://t.me) — canal x" {
		t.Errorf("last line = %q, want the appended link", last)
	}
	if !strings.Contains(string(data), "intro") {
		t.Error("AppendLine destroyed the previous content")
	}
}

func TestAppendLineRejectsPathOutsideVault(t *testing.T) {
	s := newTestStore(t, 0)
	if err := s.AppendLine("../outside.md", "x"); err == nil {
		t.Error("AppendLine(../outside.md) = nil error, want rejection")
	}
}

func TestSlug(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"Ideas de octubre", "ideas-de-octubre"},
		{"  ¿Qué Hacer?  ", "qu-hacer"},
		{"---", "nota"},
		{strings.Repeat("a", 200), strings.Repeat("a", 80)},
	} {
		if got := Slug(tc.in); got != tc.want {
			t.Errorf("Slug(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
