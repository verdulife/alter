package telegram

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
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

func TestEscapeHTML(t *testing.T) {
	tests := []struct {
		input, want string
	}{
		{"hello", "hello"},
		{"<b>bold</b>", "&lt;b&gt;bold&lt;/b&gt;"},
		{"a & b", "a &amp; b"},
		{`"quoted"`, "&quot;quoted&quot;"},
		{"it's", "it&#39;s"},
		{"<script>alert(1)</script>", "&lt;script&gt;alert(1)&lt;/script&gt;"},
	}
	for _, tt := range tests {
		if got := escapeHTML(tt.input); got != tt.want {
			t.Errorf("escapeHTML(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestMarkdownToHTMLEscapesAndFormats(t *testing.T) {
	got := MarkdownToHTML("# Título\n\n**negrita** y *cursiva* y `codigo`\n\n[enlace](https://t.me/?a=1&b=2)\n\n<script>alert(1)</script>")
	for _, want := range []string{
		// Classic Telegram HTML has no heading tags: bold is the closest legal
		// rendering, so # lines become <b>.
		"<b>Título</b>",
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

// TestSendFormattedFallsBackToHTML pins the degradation contract: when Telegram
// rejects the rich message, the same content is delivered as classic HTML split
// into legal message sizes instead of failing the turn.
func TestSendFormattedFallsBackToHTML(t *testing.T) {
	bodies := map[string]string{}
	var paths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		body, _ := io.ReadAll(r.Body)
		bodies[r.URL.Path] = string(body)
		if strings.HasSuffix(r.URL.Path, "/sendRichMessage") {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(w, `{"ok":false,"error_code":400,"description":"Bad Request: can't parse rich message"}`)
			return
		}
		_, _ = io.WriteString(w, `{"ok":true,"result":{"message_id":1,"chat":{"id":7}}}`)
	}))
	t.Cleanup(srv.Close)

	c := NewClientWithBase("TOKEN", srv.URL)
	if err := c.SendFormatted(context.Background(), 7, "**hola**"); err != nil {
		t.Fatalf("SendFormatted: %v", err)
	}

	if len(paths) != 2 {
		t.Fatalf("paths = %v, want [sendRichMessage sendMessage]", paths)
	}
	if !strings.HasSuffix(paths[0], "/sendRichMessage") || !strings.HasSuffix(paths[1], "/sendMessage") {
		t.Fatalf("paths = %v, want rich first then HTML fallback", paths)
	}
	html := bodies["/botTOKEN/sendMessage"]
	// encoding/json escapes '<' as \u003c on the wire, so the literal is compared
	// in its encoded form.
	if !strings.Contains(html, `\u003cb\u003ehola\u003c/b\u003e`) {
		t.Errorf("fallback body = %s, want escaped HTML bold", html)
	}
}
