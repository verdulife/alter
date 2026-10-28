package telegram

import (
	"context"
	"log"
	"strings"
)

// EventKind classifies what the owner sent.
type EventKind string

const (
	// EventText is a plain or formatted text message.
	EventText EventKind = "text"
	// EventDocument is a file attachment.
	EventDocument EventKind = "document"
	// EventPhoto is an image sent as a photo.
	EventPhoto EventKind = "photo"
	// EventCallback is an inline-button press.
	EventCallback EventKind = "callback"
)

// Event is the normalized inbound unit the bridge consumes. It deliberately
// flattens Telegram's wire types so the bridge never touches Bot API structs.
type Event struct {
	Kind     EventKind
	ChatID   int64
	Text     string
	Caption  string
	FileID   string
	FileName string
	MimeType string
	FileSize int64

	// Callback fields, only for EventCallback.
	CallbackID   string
	CallbackData string
}

// EventHandler processes one inbound event. An error is logged and the poll
// loop continues: a single broken turn must never stop the channel.
type EventHandler func(ctx context.Context, ev Event) error

// updateSource is the polling seam, so tests do not need HTTP.
type updateSource interface {
	GetUpdates(ctx context.Context, offset int) ([]Update, error)
}

// Poller long-polls the Bot API and dispatches normalized events for the single
// owner chat. Everything from another chat is dropped before it reaches the
// handler: Alter has exactly one user.
type Poller struct {
	source  updateSource
	ownerID int64
	logger  *log.Logger
	handler EventHandler
}

// NewPoller builds a Poller for the owner chat.
func NewPoller(source updateSource, ownerID int64, logger *log.Logger, handler EventHandler) *Poller {
	if logger == nil {
		logger = log.Default()
	}
	return &Poller{source: source, ownerID: ownerID, logger: logger, handler: handler}
}

// Run polls until ctx is cancelled. Poll failures are logged and retried; they
// are expected during network blips and must not tear the process down.
func (p *Poller) Run(ctx context.Context) error {
	offset := 0
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		updates, err := p.source.GetUpdates(ctx, offset)
		if err != nil {
			p.logger.Printf("telegram: getUpdates: %v", err)
			continue
		}
		for _, u := range updates {
			if u.ID >= offset {
				offset = u.ID + 1
			}
			p.Handle(ctx, u)
		}
	}
}

// Handle dispatches a single update. It is exported so the poll loop can be
// tested without spinning a goroutine.
func (p *Poller) Handle(ctx context.Context, u Update) {
	ev, ok := p.normalize(u)
	if !ok {
		return
	}
	if err := p.handler(ctx, ev); err != nil {
		p.logger.Printf("telegram: handler (%s): %v", ev.Kind, err)
	}
}

// normalize converts a Telegram update into an Event, dropping anything the
// owner did not send or that carries no usable content.
func (p *Poller) normalize(u Update) (Event, bool) {
	if cb := u.CallbackQuery; cb != nil {
		if cb.From != nil && p.ownerID != 0 && cb.From.ID != p.ownerID {
			return Event{}, false
		}
		ev := Event{Kind: EventCallback, CallbackID: cb.ID, CallbackData: cb.Data}
		if cb.Message != nil {
			ev.ChatID = cb.Message.Chat.ID
		}
		return ev, true
	}

	msg := u.Message
	if msg == nil {
		return Event{}, false
	}
	if p.ownerID != 0 && msg.Chat.ID != p.ownerID {
		return Event{}, false
	}

	switch {
	case msg.Document != nil:
		return Event{
			Kind:     EventDocument,
			ChatID:   msg.Chat.ID,
			Caption:  msg.Caption,
			FileID:   msg.Document.FileID,
			FileName: msg.Document.FileName,
			MimeType: msg.Document.MimeType,
			FileSize: msg.Document.FileSize,
		}, true
	case len(msg.Photo) > 0:
		// Telegram sends every resolution; the last one is the largest.
		largest := msg.Photo[len(msg.Photo)-1]
		return Event{
			Kind:     EventPhoto,
			ChatID:   msg.Chat.ID,
			Caption:  msg.Caption,
			FileID:   largest.FileID,
			FileName: "photo.jpg",
		}, true
	case strings.TrimSpace(msg.Text) != "":
		return Event{Kind: EventText, ChatID: msg.Chat.ID, Text: msg.Text}, true
	}
	return Event{}, false
}
