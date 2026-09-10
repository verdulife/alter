package telegram

import (
	"fmt"
	"strings"
)

// escapeHTML escapes the five XML-significant characters so user-supplied text
// can be safely embedded in Telegram HTML messages (<b>, <i>, etc.).
func escapeHTML(s string) string {
	s = strings.ReplaceAll(s, "&", "&amp;")
	s = strings.ReplaceAll(s, "<", "&lt;")
	s = strings.ReplaceAll(s, ">", "&gt;")
	s = strings.ReplaceAll(s, "\"", "&quot;")
	s = strings.ReplaceAll(s, "'", "&#39;")
	return s
}

// --- Task creation ----------------------------------------------------------

// MsgTaskCreated returns the confirmation after a plain task is created.
func MsgTaskCreated(title string) string {
	return fmt.Sprintf("Tarea creada: <b>%s</b>", escapeHTML(title))
}

// --- Reminder creation ------------------------------------------------------

// MsgReminderCreated returns the confirmation after a reminder is scheduled.
func MsgReminderCreated(title, when string) string {
	return fmt.Sprintf("⏰ <b>%s</b>\npara %s", escapeHTML(title), escapeHTML(when))
}

// --- Reminder fired (scheduler → Channel) ------------------------------------

// MsgReminderFired composes the plain semantic content of a fired-reminder
// notification from its parts (task title + optional description). It carries
// no Telegram presentation: Channel.Send applies the HTML framing at send time,
// so the same content format is shared by the non-Pi path (NotifyAction with a
// description) and the Pi path (Orchestrator sending the agent's plain reply
// through Channel.Send).
func MsgReminderFired(title, body string) string {
	if body == "" {
		return title
	}
	return title + "\n\n" + body
}

// frameNotification applies the Telegram HTML framing used for all
// application-initiated notifications: a ⏰ prefix, a bold first line (the
// notification subject) and the remaining plain text as body. Producers only
// send semantic plain text; this adapter owns presentation, so both parts are
// escaped before they reach the HTML-parsed send.
func frameNotification(text string) string {
	title, body, _ := strings.Cut(text, "\n\n")
	var sb strings.Builder
	sb.WriteString("⏰ <b>" + escapeHTML(title) + "</b>")
	if body != "" {
		sb.WriteString("\n\n" + escapeHTML(body))
	}
	return sb.String()
}

// --- Task list --------------------------------------------------------------

// MsgTaskListEmpty returns the response when there are no pending tasks.
func MsgTaskListEmpty() string {
	return "No hay tareas pendientes."
}

// MsgTaskList returns a formatted list of pending task titles.
// Each item in items is already a plain task title.
func MsgTaskList(items []string) string {
	count := len(items)
	header := fmt.Sprintf("%d tarea(s):", count)
	var sb strings.Builder
	sb.WriteString(header)
	for i, t := range items {
		sb.WriteString(fmt.Sprintf("\n%d) %s", i+1, escapeHTML(t)))
	}
	return sb.String()
}

// --- Task completed ---------------------------------------------------------

// MsgTaskCompleted returns the confirmation after a task is marked completed.
func MsgTaskCompleted(title string) string {
	return fmt.Sprintf("✅ <b>%s</b> completada", escapeHTML(title))
}

// --- Task cancelled ---------------------------------------------------------

// MsgTaskCancelled returns the confirmation after a task is cancelled.
func MsgTaskCancelled(title string) string {
	return fmt.Sprintf("❌ <b>%s</b> cancelada", escapeHTML(title))
}

// --- Task resolution errors -------------------------------------------------

// MsgNoMatch returns the message when a task reference matches nothing.
func MsgNoMatch(ref string) string {
	return fmt.Sprintf("No encontré ninguna tarea pendiente que coincida con «%s».", escapeHTML(ref))
}

// MsgMultipleMatches returns the disambiguation message when a reference
// matches more than one pending task.
func MsgMultipleMatches(ref string, titles []string) string {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Varias opciones para «%s»:", escapeHTML(ref)))
	for i, t := range titles {
		sb.WriteString(fmt.Sprintf("\n%d) %s", i+1, escapeHTML(t)))
	}
	sb.WriteString("\n¿Cuál?")
	return sb.String()
}

// --- Generic errors ---------------------------------------------------------

// MsgErrorAction returns a generic "could not do X" message.
func MsgErrorAction(action string) string {
	return fmt.Sprintf("No pude %s. Intentá de nuevo.", escapeHTML(action))
}

// MsgErrorCreating returns a creation error with detail.
func MsgErrorCreating(kind string, detail string) string {
	return fmt.Sprintf("No pude crear %s: %s", escapeHTML(kind), escapeHTML(detail))
}

// MsgErrorWithDetail returns a generic error with detail.
func MsgErrorWithDetail(action, detail string) string {
	return fmt.Sprintf("No pude %s: %s", escapeHTML(action), escapeHTML(detail))
}

// --- Help -------------------------------------------------------------------

// MsgHelp returns a concise help summary.
func MsgHelp() string {
	return "Hola, soy ALTER.\n\n" +
		"Puedo crear tareas y recordatorios.\n\n" +
		"Ejemplos:\n" +
		"• comprar SSD\n" +
		"• recordar comprar SSD en 30 minutos\n" +
		"• /listar"
}

// --- Slash command usage hints ----------------------------------------------

// MsgUsageNueva returns the usage hint for /nueva.
func MsgUsageNueva() string {
	return "Uso: /nueva &lt;título&gt;"
}

// MsgUsageRecordar returns the usage hint for /recordar.
func MsgUsageRecordar() string {
	return "Uso: /recordar &lt;título&gt; in &lt;duración&gt; (p. ej. 10s, 5m, 1h)"
}

// MsgUsageCompletar returns the usage hint for /completar.
func MsgUsageCompletar() string {
	return "Uso: /completar &lt;referencia de la tarea&gt;"
}

// MsgUsageCancelar returns the usage hint for /cancelar.
func MsgUsageCancelar() string {
	return "Uso: /cancelar &lt;referencia de la tarea&gt;"
}

// --- Unrecognized -----------------------------------------------------------

// MsgUnrecognized returns the fallback for NL messages that don't match any
// intent and for unknown slash commands when no natural handler is configured.
func MsgUnrecognized() string {
	return MsgHelp()
}

// --- NL-specific errors -----------------------------------------------------

// MsgNLErrInterpret returns the error when Pi fails to parse a message.
func MsgNLErrInterpret() string {
	return "No pude entender tu mensaje. Intentá con más detalle."
}

// MsgNLErrResolveTime returns the error when a time expression can't be resolved.
func MsgNLErrResolveTime(detail string) string {
	return fmt.Sprintf("No pude resolver el horario: %s", escapeHTML(detail))
}

// MsgNLErrNoTimeSpecified returns the error when a reminder lacks a time.
func MsgNLErrNoTimeSpecified() string {
	return "No pude entender cuándo quieres el recordatorio."
}

// MsgNLErrUnsupportedAction returns the error for an unrecognized action code.
func MsgNLErrUnsupportedAction(action string) string {
	return fmt.Sprintf("Acción no soportada: %s", escapeHTML(action))
}

// MsgNLErrTriggerPartial returns the error when a task was created but the
// trigger could not be scheduled.
func MsgNLErrTriggerPartial(detail string) string {
	return fmt.Sprintf("Tarea creada, pero no pude programar el recordatorio: %s", escapeHTML(detail))
}
