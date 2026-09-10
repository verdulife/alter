package telegram

import (
	"strings"
	"testing"
)

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

func TestMsgTaskCreated(t *testing.T) {
	msg := MsgTaskCreated("comprar SSD")
	if !strings.Contains(msg, "<b>comprar SSD</b>") {
		t.Errorf("want bold title, got %q", msg)
	}
	if !strings.HasPrefix(msg, "Tarea creada:") {
		t.Errorf("want prefix, got %q", msg)
	}
}

func TestMsgTaskCreatedEscapesTitle(t *testing.T) {
	msg := MsgTaskCreated("comprar <SSD> & 'más'")
	if strings.Contains(msg, "<SSD>") {
		t.Errorf("title not escaped: %q", msg)
	}
	if !strings.Contains(msg, "&lt;SSD&gt;") {
		t.Errorf("want escaped angle brackets, got %q", msg)
	}
}

func TestMsgReminderCreated(t *testing.T) {
	msg := MsgReminderCreated("comprar SSD", "hoy a las 20:00")
	if !strings.Contains(msg, "<b>comprar SSD</b>") {
		t.Errorf("want bold title, got %q", msg)
	}
	if !strings.Contains(msg, "para hoy a las 20:00") {
		t.Errorf("want 'para' + when, got %q", msg)
	}
	if !strings.HasPrefix(msg, "⏰") {
		t.Errorf("want emoji prefix, got %q", msg)
	}
}

func TestMsgReminderFired(t *testing.T) {
	msg := MsgReminderFired("comprar SSD", "Es hora de comprar el SSD.")
	if msg != "comprar SSD\n\nEs hora de comprar el SSD." {
		t.Errorf("want plain semantic content, got %q", msg)
	}
}

func TestMsgReminderFiredEmptyBody(t *testing.T) {
	msg := MsgReminderFired("comprar SSD", "")
	if msg != "comprar SSD" {
		t.Errorf("want title only, got %q", msg)
	}
}

func TestFrameNotification(t *testing.T) {
	msg := frameNotification("comprar SSD\n\nEs hora de comprar el SSD.")
	if msg != "⏰ <b>comprar SSD</b>\n\nEs hora de comprar el SSD." {
		t.Errorf("want framed message, got %q", msg)
	}
}

func TestFrameNotificationSingleLine(t *testing.T) {
	msg := frameNotification("Es hora de comprar el SSD.")
	if msg != "⏰ <b>Es hora de comprar el SSD.</b>" {
		t.Errorf("want framed single line, got %q", msg)
	}
	if strings.Contains(msg, "\n\n") {
		t.Errorf("single line must not gain a body separator: %q", msg)
	}
}

func TestFrameNotificationSplitsFirstSeparatorOnly(t *testing.T) {
	msg := frameNotification("Título\n\nPárrafo 1\n\nPárrafo 2")
	if !strings.HasPrefix(msg, "⏰ <b>Título</b>\n\n") {
		t.Errorf("title must be the first line, got %q", msg)
	}
	if !strings.Contains(msg, "Párrafo 1\n\nPárrafo 2") {
		t.Errorf("rest must keep later separators, got %q", msg)
	}
}

func TestFrameNotificationEscapesContent(t *testing.T) {
	msg := frameNotification("comprar <SSD> & más\n\n50% & 100%")
	if strings.Contains(msg, "<SSD>") {
		t.Errorf("title not escaped: %q", msg)
	}
	if strings.Contains(msg, "& más") {
		t.Errorf("title ampersand not escaped: %q", msg)
	}
	if strings.Contains(msg, "50% & 100%") {
		t.Errorf("body not escaped: %q", msg)
	}
	if !strings.HasPrefix(msg, "⏰ <b>comprar &lt;SSD&gt; &amp; más</b>") {
		t.Errorf("want escaped framed title, got %q", msg)
	}
}

func TestMsgReminderCreatedEscapesHTML(t *testing.T) {
	msg := MsgReminderCreated("comprar <SSD>", "20:00")
	if strings.Contains(msg, "<SSD>") {
		t.Errorf("title not escaped: %q", msg)
	}
}

func TestMsgTaskListEmpty(t *testing.T) {
	msg := MsgTaskListEmpty()
	if msg == "" {
		t.Error("expected non-empty message")
	}
}

func TestMsgTaskList(t *testing.T) {
	msg := MsgTaskList([]string{"comprar SSD", "llamar al fontanero"})
	if !strings.Contains(msg, "2 tarea(s):") {
		t.Errorf("want count, got %q", msg)
	}
	if !strings.Contains(msg, "1) comprar SSD") {
		t.Errorf("want first item, got %q", msg)
	}
	if !strings.Contains(msg, "2) llamar al fontanero") {
		t.Errorf("want second item, got %q", msg)
	}
}

func TestMsgTaskListEscapesTitles(t *testing.T) {
	msg := MsgTaskList([]string{"comprar <SSD>"})
	if strings.Contains(msg, "<SSD>") {
		t.Errorf("title not escaped: %q", msg)
	}
}

func TestMsgTaskCompleted(t *testing.T) {
	msg := MsgTaskCompleted("comprar SSD")
	if !strings.Contains(msg, "✅") {
		t.Errorf("want check emoji, got %q", msg)
	}
	if !strings.Contains(msg, "<b>comprar SSD</b>") {
		t.Errorf("want bold title, got %q", msg)
	}
	if !strings.Contains(msg, "completada") {
		t.Errorf("want 'completada', got %q", msg)
	}
}

func TestMsgTaskCancelled(t *testing.T) {
	msg := MsgTaskCancelled("comprar SSD")
	if !strings.Contains(msg, "❌") {
		t.Errorf("want cross emoji, got %q", msg)
	}
	if !strings.Contains(msg, "<b>comprar SSD</b>") {
		t.Errorf("want bold title, got %q", msg)
	}
	if !strings.Contains(msg, "cancelada") {
		t.Errorf("want 'cancelada', got %q", msg)
	}
}

func TestMsgNoMatch(t *testing.T) {
	msg := MsgNoMatch("SSD")
	if !strings.Contains(msg, "SSD") {
		t.Errorf("want ref in message, got %q", msg)
	}
	if !strings.Contains(msg, "No encontré") {
		t.Errorf("want 'No encontré', got %q", msg)
	}
}

func TestMsgNoMatchEscapesRef(t *testing.T) {
	msg := MsgNoMatch("<script>")
	if strings.Contains(msg, "<script>") {
		t.Errorf("ref not escaped: %q", msg)
	}
}

func TestMsgMultipleMatches(t *testing.T) {
	msg := MsgMultipleMatches("SSD", []string{"SSD negro", "SSD blanco"})
	if !strings.Contains(msg, "Varias opciones") {
		t.Errorf("want 'Varias opciones', got %q", msg)
	}
	if !strings.Contains(msg, "1) SSD negro") {
		t.Errorf("want first item, got %q", msg)
	}
	if !strings.Contains(msg, "2) SSD blanco") {
		t.Errorf("want second item, got %q", msg)
	}
	if !strings.HasSuffix(msg, "¿Cuál?") {
		t.Errorf("want '¿Cuál?' suffix, got %q", msg)
	}
}

func TestMsgMultipleMatchesEscapesRef(t *testing.T) {
	msg := MsgMultipleMatches("<b>X</b>", []string{"a"})
	if strings.Contains(msg, "<b>X</b>") {
		t.Errorf("ref not escaped: %q", msg)
	}
}

func TestMsgHelp(t *testing.T) {
	msg := MsgHelp()
	if !strings.Contains(msg, "ALTER") {
		t.Errorf("want ALTER in help, got %q", msg)
	}
}

func TestMsgUsageHints(t *testing.T) {
	msgs := []string{MsgUsageNueva(), MsgUsageRecordar(), MsgUsageCompletar(), MsgUsageCancelar()}
	for i, msg := range msgs {
		if msg == "" {
			t.Errorf("usage hint %d is empty", i)
		}
	}
}
