package telegram

import (
	"context"
	"errors"
	"regexp"
	"strings"
	"unicode/utf8"
)

// Documented Rich Messages limits (Bot API 10.1+). Exceeding any of them makes
// Telegram reject the whole message, so the sender checks before spending the
// round trip and degrades to classic HTML when the content does not fit.
const (
	maxRichChars    = 32768
	maxRichBlocks   = 500
	maxTableColumns = 20

	// maxHTMLChars is the classic sendMessage limit; the fallback splits below it
	// to leave room for the tags MarkdownToHTML adds.
	maxHTMLChars = 4096
	// htmlChunkTarget keeps a margin under maxHTMLChars.
	htmlChunkTarget = 3500
)

var (
	reHeading = regexp.MustCompile(`^(#{1,6})\s+(.*)$`)
	reBold    = regexp.MustCompile(`\*\*(.+?)\*\*`)
	reItalic  = regexp.MustCompile(`(^|[\s(])\*([^*\n]+)\*`)
	reCode    = regexp.MustCompile("`([^`\n]+)`")
	reLink    = regexp.MustCompile(`\[([^\]\n]+)\]\(([^)\s]+)\)`)
)

// RichFits reports whether markdown can be sent as a rich message without
// exceeding Telegram's documented limits: 32768 UTF-8 characters, 500 blocks
// (approximated by non-empty lines, the finest granularity available to us) and
// 20 columns in any table.
func RichFits(markdown string) bool {
	if markdown == "" {
		return false
	}
	if utf8.RuneCountInString(markdown) > maxRichChars {
		return false
	}

	blocks := 0
	for _, line := range strings.Split(markdown, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		blocks++
		if blocks > maxRichBlocks {
			return false
		}
		if columns, ok := tableColumns(line); ok && columns > maxTableColumns {
			return false
		}
	}
	return true
}

// tableColumns returns the column count when the line is a markdown table row.
func tableColumns(line string) (int, bool) {
	if !strings.HasPrefix(line, "|") {
		return 0, false
	}
	trimmed := strings.Trim(line, "|")
	if strings.TrimSpace(trimmed) == "" {
		return 0, false
	}
	return len(strings.Split(trimmed, "|")), true
}

// SendFormatted delivers markdown with the richest representation Telegram
// accepts: a single rich message when it fits and is accepted, otherwise classic
// HTML split into legal chunks. A transport failure is returned; only a rejected
// rich message triggers the fallback.
func (c *Client) SendFormatted(ctx context.Context, chatID int64, markdown string) error {
	if RichFits(markdown) {
		_, err := c.SendRich(ctx, chatID, markdown)
		if err == nil {
			return nil
		}
		var apiErr *APIError
		if !errors.As(err, &apiErr) || apiErr.Code < 400 || apiErr.Code >= 500 {
			return err
		}
	}

	chunks := SplitMarkdown(markdown, htmlChunkTarget)
	if len(chunks) > 1 {
		chunks[len(chunks)-1] += "\n\n(continúa)"
	}
	for _, chunk := range chunks {
		if err := c.SendMessage(ctx, chatID, MarkdownToHTML(chunk)); err != nil {
			return err
		}
	}
	return nil
}

// SplitMarkdown splits text into chunks of at most limit runes, preferring
// line boundaries and blank-line paragraph breaks, and hard-splitting a single
// line that cannot fit on its own. Content is never dropped.
func SplitMarkdown(text string, limit int) []string {
	if limit <= 0 {
		return []string{text}
	}
	if utf8.RuneCountInString(text) <= limit {
		return []string{text}
	}

	var chunks []string
	var current strings.Builder

	flush := func() {
		if s := strings.TrimRight(current.String(), "\n"); s != "" {
			chunks = append(chunks, s)
		}
		current.Reset()
	}

	for _, line := range strings.Split(text, "\n") {
		lineLen := utf8.RuneCountInString(line)
		curLen := utf8.RuneCountInString(current.String())

		// A line that cannot fit even alone: hard-split by runes.
		if lineLen > limit {
			flush()
			runes := []rune(line)
			for len(runes) > 0 {
				n := limit
				if len(runes) < n {
					n = len(runes)
				}
				chunks = append(chunks, string(runes[:n]))
				runes = runes[n:]
			}
			continue
		}

		if curLen > 0 && curLen+1+lineLen > limit {
			flush()
		}
		if current.Len() > 0 {
			current.WriteString("\n")
		}
		current.WriteString(line)
	}
	flush()
	return chunks
}

// MarkdownToHTML renders the small markdown subset Alter emits into the HTML
// Telegram accepts. Every literal character is escaped first, so a model reply
// can never inject tags.
func MarkdownToHTML(markdown string) string {
	var out strings.Builder
	inFence := false

	for _, raw := range strings.Split(markdown, "\n") {
		line := raw
		if strings.HasPrefix(strings.TrimSpace(line), "```") {
			if inFence {
				out.WriteString("</pre>")
				inFence = false
			} else {
				out.WriteString("<pre>")
				inFence = true
			}
			out.WriteString("\n")
			continue
		}
		if inFence {
			out.WriteString(escapeHTML(line))
			out.WriteString("\n")
			continue
		}

		if m := reHeading.FindStringSubmatch(strings.TrimSpace(line)); m != nil {
			out.WriteString("<b>")
			out.WriteString(inlineHTML(m[2]))
			out.WriteString("</b>\n")
			continue
		}

		trimmed := strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(trimmed, "- ") || strings.HasPrefix(trimmed, "* ") || strings.HasPrefix(trimmed, "+ "):
			out.WriteString("• " + inlineHTML(trimmed[2:]) + "\n")
		case trimmed == "---" || trimmed == "***":
			out.WriteString("———\n")
		default:
			out.WriteString(inlineHTML(line))
			out.WriteString("\n")
		}
	}
	if inFence {
		out.WriteString("</pre>")
	}
	return strings.TrimRight(out.String(), "\n")
}

// inlineHTML escapes a line and then applies the inline markdown constructs.
// Escaping happens first, so the replacements below cannot be spoofed by input.
func inlineHTML(line string) string {
	s := escapeHTML(line)
	s = reLink.ReplaceAllString(s, `<a href="$2">$1</a>`)
	s = reCode.ReplaceAllString(s, `<code>$1</code>`)
	s = reBold.ReplaceAllString(s, `<b>$1</b>`)
	s = reItalic.ReplaceAllString(s, `$1<i>$2</i>`)
	return s
}
