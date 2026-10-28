package telegram

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestRichFitsRejectsOversizedPayloads(t *testing.T) {
	exact := strings.Repeat("a", maxRichChars)
	if !RichFits(exact) {
		t.Error("RichFits(32768) = false, want true")
	}
	if RichFits(exact + "a") {
		t.Error("RichFits(32769) = true, want false")
	}

	// 21 columns: a table header plus the separator row is not enough, the data
	// row defines the width Telegram measures.
	cols := make([]string, maxTableColumns+1)
	for i := range cols {
		cols[i] = "c"
	}
	wide := "| " + strings.Join(cols, " | ") + " |\n|" + strings.Repeat("---|", len(cols)) + "\n"
	if RichFits(wide) {
		t.Errorf("RichFits(21 columns) = true, want false")
	}

	// One line per block, well past the 500 block budget.
	var many strings.Builder
	for i := 0; i < maxRichBlocks+1; i++ {
		many.WriteString("parrafo\n\n")
	}
	if RichFits(many.String()) {
		t.Error("RichFits(501 blocks) = true, want false")
	}
}

func TestMarkdownToHTMLEscapesAndFormats(t *testing.T) {
	got := MarkdownToHTML("# Título\n\n**negrita** y *cursiva* y `codigo`\n\n[enlace](https://t.me/?a=1&b=2)\n\n<script>alert(1)</script>")
	for _, want := range []string{
		"<h1>Título</h1>",
		"<b>negrita</b>",
		"<i>cursiva</i>",
		"<code>codigo</code>",
		`<a href="https://t.me/?a=1&amp;b=2">enlace</a>`,
		"&lt;script&gt;alert(1)&lt;/script&gt;",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("MarkdownToHTML missing %q in:\n%s", want, got)
		}
	}
	if strings.Contains(got, "<script>") {
		t.Error("MarkdownToHTML leaked a raw script tag")
	}
}

func TestSplitMarkdownRespectsLimitAndLines(t *testing.T) {
	var sb strings.Builder
	for i := 0; i < 200; i++ {
		sb.WriteString("linea numero uno con texto suficiente para forzar varios trozos\n\n")
	}
	chunks := SplitMarkdown(sb.String(), 3500)
	if len(chunks) < 2 {
		t.Fatalf("chunks = %d, want >= 2", len(chunks))
	}
	for i, c := range chunks {
		if n := utf8.RuneCountInString(c); n > 3500 {
			t.Errorf("chunk %d has %d runes, want <= 3500", i, n)
		}
		if i > 0 && strings.HasPrefix(c, "\n") {
			t.Errorf("chunk %d starts with a newline", i)
		}
	}
	// Rejoining must not lose content.
	if got, want := strings.Count(strings.Join(chunks, "\n"), "linea numero"), 200; got != want {
		t.Errorf("lines kept = %d, want %d", got, want)
	}
}

func TestSplitMarkdownBreaksUnbreakableLine(t *testing.T) {
	long := strings.Repeat("x", 40)
	chunks := SplitMarkdown(long, 10)
	if len(chunks) != 4 {
		t.Fatalf("chunks = %d, want 4", len(chunks))
	}
	for i, c := range chunks {
		if utf8.RuneCountInString(c) > 10 {
			t.Errorf("chunk %d = %d runes, want <= 10", i, utf8.RuneCountInString(c))
		}
	}
	if strings.Join(chunks, "") != long {
		t.Error("splitting an unbreakable line lost content")
	}
}
