package telegram

import (
	"context"

	"github.com/verdu/alter/internal/domain"
)

// NotifyAction is the concrete domain.TriggerAction that delivers a fired trigger
// as a Telegram notification to the owner chat via a domain.Channel.
//
// Responsibilities are intentionally minimal: it only builds a user-facing
// message from the Trigger+Task and calls Channel.Send. It does NOT persist fire
// state, schedule, retry, or emit events — those are owned by the Scheduler and
// the domain. On Channel.Send failure it returns the error so the Scheduler can
// apply its RetryAt backoff without consuming the trigger.
type NotifyAction struct {
	channel domain.Channel
}

var _ domain.TriggerAction = (*NotifyAction)(nil)

// NewNotifyAction builds a TriggerAction that notifies via the given Channel.
func NewNotifyAction(channel domain.Channel) *NotifyAction {
	return &NotifyAction{channel: channel}
}

// Execute sends the notification message for a fired trigger to the owner chat.
func (a *NotifyAction) Execute(ctx context.Context, _ domain.Trigger, task domain.Task) error {
	return a.channel.Send(ctx, notifyMessage(task))
}

// notifyMessage renders the user-facing notification text from a task.
func notifyMessage(task domain.Task) string {
	msg := "⏰ Recordatorio: " + task.Title
	if task.Description != "" {
		msg += "\n" + task.Description
	}
	return msg
}
