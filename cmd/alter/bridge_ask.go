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

// errApprovalNoAnswer reports that an approval prompt expired without a press.
// It is returned as an error on purpose: an approval has no "sin respuesta"
// result, because silence must block the action instead of being reported as a
// normal outcome the model can talk its way around.
var errApprovalNoAnswer = errors.New("approval: the owner did not answer, so the action is blocked")

// askKind selects how one question's buttons are rendered. It is presentation
// only: the callback payload, the pending slot and the acknowledgement path are
// identical for both kinds, so a restyle can never change what a press means.
type askKind int

const (
	// askKindOptions is an ask_options question: two to four neutral choices.
	askKindOptions askKind = iota
	// askKindApproval is an approval prompt: approve or reject one action.
	askKindApproval
)

// approvalOptions are the only two answers to an approval prompt, in index
// order: Aprobar is 0 and Rechazar is 1.
var approvalOptions = []string{"Aprobar", "Rechazar"}

// askApprovalAllowStyle is the only style the bridge sets: the affirmative button
// of an approval, so the safe answer stands out. Every other button — the
// negative one and every ask_options choice — carries no style attribute and
// keeps the client default, because inventing a colour there added contrast the
// owner did not ask for.
const askApprovalAllowStyle = "success"

// approvalIndexAllow is the button index that approves.
const approvalIndexAllow = 0

// The acknowledgements the owner sees as a toast on the press. They confirm that
// the press registered; they are not a second message, so they never edit the
// question or add a round trip.
const (
	askAckApproved = "Aprobado"
	askAckRejected = "Rechazado"

	// askAckMaxRunes bounds the toast of an ask_options press. A toast is a few
	// words on a button, so a long option is cut rather than shown in full.
	askAckMaxRunes = 40
)

// bridgeAskTransport is the Bot API surface the broker needs: one rich message
// to carry the buttons and one acknowledgement to stop the client spinner and
// show the toast of the press.
type bridgeAskTransport interface {
	SendRich(ctx context.Context, chatID int64, markdown string) (telegram.Message, error)
	AnswerCallbackQuery(ctx context.Context, callbackID, text string) error
}

// bridgeAskBroker implements tools.OptionAsker over the owner chat. It holds at
// most one pending question — an ask_options question or an approval, never
// both: AskOptions and Approve post it and wait, Resolve settles it when the
// poller reports a button press, and every other exit (deadline, cancellation)
// removes the entry so a later question can never inherit it.
type bridgeAskBroker struct {
	client bridgeAskTransport
	chatID int64
	logger *log.Logger

	mu      sync.Mutex
	pending *bridgePendingAsk
}

// bridgePendingAsk is one question waiting for a press. reply carries the chosen
// option index; it is buffered so Resolve never blocks the poll loop. labels and
// kind are kept only so a press can be acknowledged with what it chose: neither
// one changes what the press means.
type bridgePendingAsk struct {
	id      string
	labels  []string
	options int
	kind    askKind
	reply   chan int
}

// ackText is the toast the owner sees after pressing the button of this question.
// A resolved press always says what it did; a press on a question that is no
// longer pending never reaches this point.
func (p *bridgePendingAsk) ackText(index int) string {
	if p.kind == askKindApproval {
		if index == approvalIndexAllow {
			return askAckApproved
		}
		return askAckRejected
	}
	return truncateAskText(p.labels[index], askAckMaxRunes)
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

	index, err := b.ask(ctx, question, options, askKindOptions)
	if err != nil {
		if errors.Is(err, errAskBusy) || errors.Is(err, tools.ErrNoAnswer) {
			return "", err
		}
		return "", fmt.Errorf("ask_options: %w", err)
	}
	return options[index], nil
}

// Approve asks the owner to approve one destructive action and reports whether it
// may run. It is the fail-closed counterpart of AskOptions: a deadline, a
// cancelled turn, an undeliverable prompt or a busy slot all return an error and
// never a false "approved".
//
// It deliberately shares the one pending-question slot with ask_options instead
// of adding a parallel one: the product limit is exactly one pending question
// (docs/alter-limites.md), and two live prompts would make the owner's press
// ambiguous. A second caller therefore fails closed with errAskBusy.
func (b *bridgeAskBroker) Approve(ctx context.Context, question string) (bool, error) {
	if strings.TrimSpace(question) == "" {
		return false, errors.New("approve needs a question")
	}

	index, err := b.ask(ctx, question, approvalOptions, askKindApproval)
	if err != nil {
		switch {
		case errors.Is(err, tools.ErrNoAnswer):
			return false, errApprovalNoAnswer
		case errors.Is(err, errAskBusy):
			return false, err
		default:
			return false, fmt.Errorf("approval: %w", err)
		}
	}
	return index == approvalIndexAllow, nil
}

// ask posts one question with one button per option and waits for the press. It
// owns the single pending slot: whoever ends the wait — press, deadline or
// cancellation — the slot is free again, so a later question can never inherit it.
func (b *bridgeAskBroker) ask(ctx context.Context, question string, options []string, kind askKind) (int, error) {
	id, err := newAskID()
	if err != nil {
		return 0, err
	}
	p := &bridgePendingAsk{
		id:      id,
		labels:  append([]string(nil), options...),
		options: len(options),
		kind:    kind,
		reply:   make(chan int, 1),
	}

	b.mu.Lock()
	if b.pending != nil {
		b.mu.Unlock()
		return 0, errAskBusy
	}
	b.pending = p
	b.mu.Unlock()

	// Whatever ends the wait — press, deadline or cancellation — the slot must be
	// free again, or the next question would never be posted.
	defer b.release(p)

	if _, err := b.client.SendRich(ctx, b.chatID, askMarkdown(question, options, id, kind)); err != nil {
		// No buttons means no press: fail loudly so the caller can block the action
		// instead of waiting for an answer that cannot come.
		return 0, fmt.Errorf("send question: %w", err)
	}

	select {
	case index := <-p.reply:
		if index < 0 || index >= len(options) {
			return 0, fmt.Errorf("chosen index %d is out of range", index)
		}
		return index, nil
	case <-ctx.Done():
		return 0, tools.ErrNoAnswer
	}
}

// Resolve settles the pending question from one inbound event. A press always
// stops the client spinner, including a press that matches nothing: a stale
// button in an old message must not look broken to the owner. A press that does
// settle the question also carries a short toast saying what it chose. Only a
// press carrying the pending question's id and a valid option index settles it,
// so a late press can never answer a later question.
func (b *bridgeAskBroker) Resolve(ctx context.Context, ev telegram.Event) {
	if ev.Kind != telegram.EventCallback {
		return
	}

	id, index, ok := parseAskCallback(ev.CallbackData)

	b.mu.Lock()
	p := b.pending
	var reply chan int
	ackText := ""
	if ok && p != nil && p.id == id && index < p.options {
		// Consumed: one press settles one question, and the slot is free at once.
		b.pending = nil
		reply = p.reply
		ackText = p.ackText(index)
	}
	b.mu.Unlock()

	// Exactly one acknowledgement per press: it is what stops the spinner, and the
	// text it carries is the only feedback the owner gets besides the answer
	// arriving. A press that settles nothing is acknowledged without it.
	if ev.CallbackID != "" {
		if err := b.client.AnswerCallbackQuery(ctx, ev.CallbackID, ackText); err != nil {
			// Cosmetic only: the answer still reaches the waiting turn.
			b.logger.Printf("bridge: answer callback: %v", err)
		}
	}

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
		return "", fmt.Errorf("generate question id: %w", err)
	}
	return hex.EncodeToString(buf), nil
}

// askMarkdown renders the question and its buttons as one rich message. Rich
// messages are the only representation that carries callback buttons, so the
// bridge does not fall back to classic HTML here: a question without buttons
// would be a question the owner cannot answer.
//
// Every button gets its own row, because a button that shares a row is half as
// wide and on a phone these are the words the owner has to hit. kind only decides
// whether the affirmative button is styled success; the label is always the
// option text and the payload inside each button is the same for both kinds.
func askMarkdown(question string, options []string, id string, kind askKind) string {
	var out strings.Builder
	out.WriteString(strings.TrimSpace(question))
	out.WriteString("\n\n")
	for i, option := range options {
		out.WriteString("<tg-button-row>")
		out.WriteString(`<tg-button type="callback_data"`)
		if kind == askKindApproval && i == approvalIndexAllow {
			out.WriteString(` style="`)
			out.WriteString(askApprovalAllowStyle)
			out.WriteString(`"`)
		}
		out.WriteString(` data="`)
		out.WriteString(askCallbackData(id, i))
		out.WriteString(`">`)
		out.WriteString(escapeAskLabel(option))
		out.WriteString("</tg-button>")
		out.WriteString("</tg-button-row>\n")
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

// escapeAskText keeps the question text inside the rich message. The renderer
// reads markup, so the three special characters are escaped; control characters
// are flattened but line breaks are kept, because a question may span lines. Tool
// arguments are model-controlled, so this is what stops a command from opening a
// button tag of its own.
func escapeAskText(text string) string {
	text = strings.Map(func(r rune) rune {
		if r == '\n' {
			return r
		}
		if r < 0x20 || r == 0x7f {
			return ' '
		}
		return r
	}, text)
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;").Replace(text)
}

// truncateAskText caps text at limit runes and marks the cut, so the owner knows
// the question shows only part of what may run.
func truncateAskText(text string, limit int) string {
	runes := []rune(text)
	if len(runes) <= limit {
		return text
	}
	return string(runes[:limit]) + "…"
}
