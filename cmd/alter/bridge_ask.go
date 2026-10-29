package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log"
	"strconv"
	"strings"
	"sync"

	"github.com/verdu/alter/internal/adapter/telegram"
	"github.com/verdu/alter/internal/tools"
)

// Inline buttons are the phase-1 way to ask the owner a question (S3): Pi calls
// ask_options, the bridge posts the question with 2..4 callback buttons in a
// rich message and blocks until the press arrives through the poller. A press is
// never free text, so it can never re-enter Pi as a prompt.
const (
	// askCallbackPrefix marks the callback data this bridge generated, so
	// foreign presses (another bot, an edited message) are recognisably not
	// ours.
	askCallbackPrefix = "ask"

	// askIDBytes is the width of the per-question random id. Eight bytes keep
	// the whole callback payload at 22 bytes, well under Telegram's 64-byte
	// limit, and make two questions collide only by accident of 2^-64.
	askIDBytes = 8

	// askMaxCallbackBytes is Telegram's hard limit on callback_data.
	askMaxCallbackBytes = 64

	// bridgeAskTimeout is the longest one ask_options wait may block the turn.
	// It mirrors the tools contract (10 minutes) and is applied by the tools
	// server as the deadline around the broker's wait.
	bridgeAskTimeout = tools.DefaultAskTimeout
)

// errAskBusy reports that ask_options was called while another question is
// still waiting for the owner. Failing fast is deliberate: two live questions
// would leave the owner pressing buttons for a question that already gave up,
// and the model would receive two unrelated answers. The second call gets a
// clear error it can explain instead.
var errAskBusy = errors.New("another question is already waiting for the owner")

// bridgeAskTransport is the Bot API surface the broker needs: one rich message
// to carry the buttons and one acknowledgement to stop the client spinner.
type bridgeAskTransport interface {
	SendRich(ctx context.Context, chatID int64, markdown string) (telegram.Message, error)
	AnswerCallbackQuery(ctx context.Context, callbackID string) error
}

// bridgeAskBroker implements tools.OptionAsker over the owner chat. It holds at
// most one pending question: AskOptions posts it and waits, Resolve settles it
// when the poller reports a button press, and every other exit (deadline,
// cancellation) removes the entry so a later question can never inherit it.
type bridgeAskBroker struct {
	client bridgeAskTransport
	chatID int64
	logger *log.Logger

	mu      sync.Mutex
	pending *bridgePendingAsk
}

// bridgePendingAsk is one question waiting for a press. reply carries the chosen
// option index; it is buffered so Resolve never blocks the poll loop.
type bridgePendingAsk struct {
	id      string
	options int
	reply   chan int
}

// newBridgeAskBroker builds a broker that posts to one owner chat.
func newBridgeAskBroker(client bridgeAskTransport, chatID int64, logger *log.Logger) *bridgeAskBroker {
	if logger == nil {
		logger = log.New(io.Discard, "", 0)
	}
	return &bridgeAskBroker{client: client, chatID: chatID, logger: logger}
}

// AskOptions implements tools.OptionAsker: it posts the question with one
// callback button per option and blocks until the matching press arrives or the
// context expires. An unanswered question is a normal outcome (ErrNoAnswer, the
// "sin respuesta" path), never a failure of the turn.
func (b *bridgeAskBroker) AskOptions(ctx context.Context, question string, options []string) (string, error) {
	if strings.TrimSpace(question) == "" {
		return "", errors.New("ask_options needs a question")
	}
	if len(options) < 2 || len(options) > 4 {
		return "", fmt.Errorf("ask_options needs between 2 and 4 options, got %d", len(options))
	}

	id, err := newAskID()
	if err != nil {
		return "", err
	}
	p := &bridgePendingAsk{id: id, options: len(options), reply: make(chan int, 1)}

	b.mu.Lock()
	if b.pending != nil {
		b.mu.Unlock()
		return "", errAskBusy
	}
	b.pending = p
	b.mu.Unlock()

	// Whatever ends the wait — press, deadline or cancellation — the slot must be
	// free again, or the next question would never be posted.
	defer b.release(p)

	if _, err := b.client.SendRich(ctx, b.chatID, askMarkdown(question, options, id)); err != nil {
		// No buttons means no press: fail loudly so the model can tell the owner
		// instead of silently waiting for an answer that cannot come.
		return "", fmt.Errorf("ask_options: send question: %w", err)
	}

	select {
	case index := <-p.reply:
		if index < 0 || index >= len(options) {
			return "", fmt.Errorf("ask_options: chosen index %d is out of range", index)
		}
		return options[index], nil
	case <-ctx.Done():
		return "", tools.ErrNoAnswer
	}
}

// Resolve settles the pending question from one inbound event. A press always
// stops the client spinner, including a press that matches nothing: a stale
// button in an old message must not look broken to the owner. Only a press
// carrying the pending question's id and a valid option index settles it, so a
// late press can never answer a later question.
func (b *bridgeAskBroker) Resolve(ctx context.Context, ev telegram.Event) {
	if ev.Kind != telegram.EventCallback {
		return
	}
	if ev.CallbackID != "" {
		if err := b.client.AnswerCallbackQuery(ctx, ev.CallbackID); err != nil {
			// Cosmetic only: the answer still reaches the waiting turn.
			b.logger.Printf("bridge: answer callback: %v", err)
		}
	}

	id, index, ok := parseAskCallback(ev.CallbackData)
	if !ok {
		return
	}

	b.mu.Lock()
	p := b.pending
	var reply chan int
	if p != nil && p.id == id && index < p.options {
		// Consumed: one press settles one question, and the slot is free at once.
		b.pending = nil
		reply = p.reply
	}
	b.mu.Unlock()

	if reply == nil {
		return
	}
	reply <- index
}

// release frees the pending slot if it still belongs to p.
func (b *bridgeAskBroker) release(p *bridgePendingAsk) {
	b.mu.Lock()
	if b.pending == p {
		b.pending = nil
	}
	b.mu.Unlock()
}

// newAskID returns the per-question random id that keeps callback data from
// colliding across questions and from carrying any of the question text.
func newAskID() (string, error) {
	buf := make([]byte, askIDBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("ask_options: generate question id: %w", err)
	}
	return hex.EncodeToString(buf), nil
}

// askMarkdown renders the question and its buttons as one rich message. Rich
// messages are the only representation that carries callback buttons, so the
// bridge does not fall back to classic HTML here: a question without buttons
// would be a question the owner cannot answer.
func askMarkdown(question string, options []string, id string) string {
	var out strings.Builder
	out.WriteString(strings.TrimSpace(question))
	out.WriteString("\n\n")
	for i, option := range options {
		out.WriteString(`<tg-button type="callback_data" data="`)
		out.WriteString(askCallbackData(id, i))
		out.WriteString(`">`)
		out.WriteString(escapeAskLabel(option))
		out.WriteString("</tg-button>\n")
	}
	return strings.TrimRight(out.String(), "\n")
}

// askCallbackData builds the opaque payload of one button: the question id plus
// the option index, and nothing derived from the vault or the question.
func askCallbackData(id string, index int) string {
	return askCallbackPrefix + ":" + id + ":" + strconv.Itoa(index)
}

// parseAskCallback splits callback data back into the question id and the option
// index, reporting anything that is not a payload this bridge produced.
func parseAskCallback(data string) (string, int, bool) {
	parts := strings.Split(data, ":")
	if len(parts) != 3 || parts[0] != askCallbackPrefix {
		return "", 0, false
	}
	if len(parts[1]) != askIDBytes*2 {
		return "", 0, false
	}
	index, err := strconv.Atoi(parts[2])
	if err != nil || index < 0 {
		return "", 0, false
	}
	return parts[1], index, true
}

// escapeAskLabel keeps an option label inside the button element: the content is
// markup, so the three special characters can never break out of it, and a
// newline is flattened so the tag stays on one line.
func escapeAskLabel(label string) string {
	label = strings.NewReplacer("\n", " ", "\r", " ", "\t", " ").Replace(strings.TrimSpace(label))
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;").Replace(label)
}
