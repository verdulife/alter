package telegram

import (
	"context"
	"log"
	"strings"
	"sync"
	"time"
)

// typingInterval is how often the adapter re-sends the "typing" chat action
// while waiting for the first streamed delta, because Telegram's typing bubble
// only lasts ~5s on its own.
const typingInterval = 4 * time.Second

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

	// Keep the typing bubble alive during the model's time-to-first-token: pi
	// can take several seconds before its first text_delta, and Telegram's
	// typing action only lasts ~5s. The keepalive stops on the first delta
	// (when the animated draft takes over) or when the handler returns.
	stopTyping := make(chan struct{})
	var stopOnce sync.Once
	stop := func() { stopOnce.Do(func() { close(stopTyping) }) }
	go a.keepaliveTyping(ctx, chatID, stopTyping)

	stream := &updateStream{
		client:       a.client,
		chatID:       chatID,
		draftID:      u.Message.MessageID,
		logger:       a.logger,
		onFirstDelta: stop,
	}
	reply, hErr := a.handler(ctx, u.Message.Text, stream)
	stop() // handler done: stop the typing keepalive

	// Finalize: the returned reply is the persisted message (sendMessage). The
	// ephemeral draft is not persisted by Telegram.
	if err := a.client.SendMessage(ctx, chatID, reply); err != nil {
		a.logger.Printf("telegram: sendMessage: %v", err)
	}
	if hErr != nil {
		a.logger.Printf("telegram: handle: %v", hErr)
	}
}

// keepaliveTyping re-sends the "typing" chat action periodically until done is
// closed or ctx is cancelled, so a slow time-to-first-token does not leave the
// chat silent.
func (a *Adapter) keepaliveTyping(ctx context.Context, chatID int64, done <-chan struct{}) {
	ticker := time.NewTicker(typingInterval)
	defer ticker.Stop()
	for {
		select {
		case <-done:
			return
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := a.client.SendChatAction(ctx, chatID, "typing"); err != nil {
				a.logger.Printf("telegram: sendChatAction (keepalive): %v", err)
			}
		}
	}
}

// updateStream publishes drafts for one incoming message. A stable, non-zero
// draftID (the originating message id) makes Telegram animate the updates.
type updateStream struct {
	client       APIClient
	chatID       int64
	draftID      int
	logger       *log.Logger
	// onFirstDelta, when non-nil, is called once on the first successful
	// draft publication so the caller can stop the typing keepalive.
	onFirstDelta func()
	once         sync.Once
}

// Update refreshes the message draft with the current partial text.
func (s *updateStream) Update(ctx context.Context, text string) error {
	s.once.Do(func() {
		if s.onFirstDelta != nil {
			s.onFirstDelta()
		}
	})
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
