package telegram

import (
	"context"

	"github.com/verdu/alter/internal/domain"
)

// Channel is the outbound notification transport. It implements domain.Channel
// for Telegram, always sending to a fixed owner chat ID configured at startup.
//
// It deliberately knows nothing about the chat that issued a particular command:
// command replies are sent directly by the inbound Adapter to the originating
// chat, while Channel carries only application-initiated notifications (e.g.
// reminders once the Scheduler is wired). This separation keeps Channel purely a
// transport with no per-command chat state.
type Channel struct {
	client  APIClient
	ownerID int64
}

var _ domain.Channel = (*Channel)(nil)

// NewChannel builds a Channel delivering notifications to the given owner chat.
func NewChannel(client APIClient, ownerID int64) *Channel {
	return &Channel{client: client, ownerID: ownerID}
}

// Name reports the transport name.
func (c *Channel) Name() string { return "telegram" }

// Send delivers a plain-text notification to the configured owner chat. The
// channel is the presentation boundary: producers (NotifyAction, Orchestrator)
// only provide semantic content, and the channel applies the Telegram HTML
// framing (see frameNotification) before sending.
func (c *Channel) Send(ctx context.Context, message string) error {
	return c.client.SendMessage(ctx, c.ownerID, frameNotification(message))
}
