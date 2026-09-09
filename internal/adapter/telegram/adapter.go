package telegram

import (
	"context"
	"log"
	"strings"
)

// Handler processes one incoming message and returns the reply text to send back
// to the originating chat.
type Handler func(ctx context.Context, text string) (string, error)

// Adapter is the inbound Telegram transport: it long-polls Bot API updates and,
// for each textual message, invokes the Handler and sends the reply directly to
// the originating chat. It never routes replies through domain.Channel, which is
// reserved for application-initiated notifications.
type Adapter struct {
	client  APIClient
	handler Handler
	logger  *log.Logger
}

// NewAdapter builds an inbound Adapter. A non-nil handler is required.
func NewAdapter(client APIClient, h Handler, logger *log.Logger) *Adapter {
	return &Adapter{client: client, handler: h, logger: logger}
}

// Run blocks until ctx is cancelled, long-polling for updates.
func (a *Adapter) Run(ctx context.Context) error {
	var offset int
	for {
		updates, err := a.client.GetUpdates(ctx, offset)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			a.logger.Printf("telegram: getUpdates: %v", err)
			continue
		}
		for _, u := range updates {
			offset = u.ID + 1
			a.handleUpdate(ctx, u)
		}
	}
}

// handleUpdate processes a single update, sending the handler's reply to the
// originating chat. Broken per-update handling never aborts the poll loop.
func (a *Adapter) handleUpdate(ctx context.Context, u Update) {
	if u.Message == nil || strings.TrimSpace(u.Message.Text) == "" {
		return
	}
	reply, hErr := a.handler(ctx, u.Message.Text)
	if err := a.client.SendMessage(ctx, u.Message.Chat.ID, reply); err != nil {
		a.logger.Printf("telegram: sendMessage: %v", err)
	}
	if hErr != nil {
		a.logger.Printf("telegram: handle: %v", hErr)
	}
}
