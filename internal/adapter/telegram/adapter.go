package telegram

import (
	"context"
	"log"
	"strings"
)

// Stream is the per-message publishing seam for streaming responses. While a
// handler generates its reply it calls Update to refresh the ephemeral message
// draft (Bot API sendMessageDraft) with the current partial text. The inbound
// Adapter always finalizes by sending the handler's returned reply via
// sendMessage, so a non-streaming handler simply never calls Update.
type Stream interface {
	// Update refreshes the message draft with the current partial text.
	Update(ctx context.Context, text string) error
}

// Handler processes one incoming message and returns the reply text to send back
// to the originating chat. The stream publishes incremental progress while the
// reply is generated; it is never nil, but a non-streaming handler may ignore it.
type Handler func(ctx context.Context, text string, stream Stream) (string, error)

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

// handleUpdate processes a single update: it gives cheap "typing" feedback,
// runs the handler with a per-message draft stream, then persists the final
// reply with sendMessage. Broken per-update handling never aborts the poll loop.
func (a *Adapter) handleUpdate(ctx context.Context, u Update) {
	if u.Message == nil || strings.TrimSpace(u.Message.Text) == "" {
		return
	}
	chatID := u.Message.Chat.ID

	// Cheap feedback while the reply is generated, before any streamed delta.
	if err := a.client.SendChatAction(ctx, chatID, "typing"); err != nil {
		a.logger.Printf("telegram: sendChatAction: %v", err)
	}

	stream := &updateStream{
		client:  a.client,
		chatID:  chatID,
		draftID: u.Message.MessageID,
		logger:  a.logger,
	}
	reply, hErr := a.handler(ctx, u.Message.Text, stream)

	// Finalize: the returned reply is the persisted message (sendMessage). The
	// ephemeral draft is not persisted by Telegram.
	if err := a.client.SendMessage(ctx, chatID, reply); err != nil {
		a.logger.Printf("telegram: sendMessage: %v", err)
	}
	if hErr != nil {
		a.logger.Printf("telegram: handle: %v", hErr)
	}
}

// updateStream publishes drafts for one incoming message. A stable, non-zero
// draftID (the originating message id) makes Telegram animate the updates.
type updateStream struct {
	client  APIClient
	chatID  int64
	draftID int
	logger  *log.Logger
}

// Update refreshes the message draft with the current partial text.
func (s *updateStream) Update(ctx context.Context, text string) error {
	draftID := s.draftID
	if draftID == 0 {
		draftID = 1 // Bot API requires a non-zero draft id
	}
	if err := s.client.SendMessageDraft(ctx, s.chatID, draftID, text); err != nil {
		s.logger.Printf("telegram: sendMessageDraft: %v", err)
		return err
	}
	return nil
}
