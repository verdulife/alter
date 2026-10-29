package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/verdu/alter/internal/adapter/telegram"
	"github.com/verdu/alter/internal/config"
	"github.com/verdu/alter/internal/tools"
	"github.com/verdu/alter/internal/vault"
)

// The ask slice is driven at two levels:
//
//   - the broker against an in-memory transport, so every protocol detail
//     (message count, button payloads, acknowledgements) is observable without
//     HTTP;
//   - the real wiring against the fakes of the text slice (fakeBot,
//     fakePiFactory), so the poller→broker route and the tools route are proven
//     end to end.
//
// reAskButtonCallback extracts the callback payload of every button the bridge
// rendered, which is exactly what Telegram would hand back on a press.
var reAskButtonCallback = regexp.MustCompile(`data="([^"]*)"`)

// askAcknowledgement is one answerCallbackQuery the broker sent: the press whose
// spinner it stops and the toast text the owner saw, empty for a silent
// acknowledgement of a press that settles nothing.
type askAcknowledgement struct {
	callbackID string
	text       string
}

// recordingAskTransport is an in-memory Bot API for the broker: it records every
// rich message and every acknowledgement, and can fail either call on demand.
type recordingAskTransport struct {
	mu      sync.Mutex
	sent    []string
	acks    []askAcknowledgement
	sendErr error
	ackErr  error
}

func newRecordingAskTransport() *recordingAskTransport {
	return &recordingAskTransport{}
}

func (r *recordingAskTransport) SendRich(_ context.Context, chatID int64, markdown string) (telegram.Message, error) {
	r.mu.Lock()
	err := r.sendErr
	if err == nil {
		r.sent = append(r.sent, markdown)
	}
	r.mu.Unlock()
	if err != nil {
		return telegram.Message{}, err
	}
	return telegram.Message{MessageID: 1, Chat: telegram.Chat{ID: chatID}}, nil
}

func (r *recordingAskTransport) AnswerCallbackQuery(_ context.Context, callbackID, text string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.ackErr != nil {
		return r.ackErr
	}
	r.acks = append(r.acks, askAcknowledgement{callbackID: callbackID, text: text})
	return nil
}

func (r *recordingAskTransport) messages() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.sent...)
}

func (r *recordingAskTransport) acknowledgements() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]string, 0, len(r.acks))
	for _, ack := range r.acks {
		out = append(out, ack.callbackID)
	}
	return out
}

// acknowledgementTexts lists the toast text of every acknowledgement, in order.
// An empty entry is a silent acknowledgement.
func (r *recordingAskTransport) acknowledgementTexts() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]string, 0, len(r.acks))
	for _, ack := range r.acks {
		out = append(out, ack.text)
	}
	return out
}

// assertAskAckToast fails unless one press was acknowledged exactly once with the
// given toast text.
func assertAskAckToast(t *testing.T, tr *recordingAskTransport, callbackID, wantText string) {
	t.Helper()
	acks := tr.acknowledgements()
	texts := tr.acknowledgementTexts()
	if len(acks) != 1 || len(texts) != 1 {
		t.Fatalf("acknowledged = %v with toast texts %q, want exactly one acknowledgement", acks, texts)
	}
	if acks[0] != callbackID {
		t.Errorf("acknowledged press = %q, want %q", acks[0], callbackID)
	}
	if texts[0] != wantText {
		t.Errorf("toast text = %q, want %q", texts[0], wantText)
	}
}

// waitMessages blocks until at least want questions were posted and returns
// them, so a test never sleeps for a fixed guess.
func (r *recordingAskTransport) waitMessages(tb testing.TB, want int) []string {
	tb.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if got := r.messages(); len(got) >= want {
			return got
		}
		time.Sleep(2 * time.Millisecond)
	}
	tb.Fatalf("timed out waiting for %d question messages, got %d: %v", want, len(r.messages()), r.messages())
	return nil
}

// askOutcome is what one AskOptions call returned.
type askOutcome struct {
	answer string
	err    error
}

// approvalOutcome is what one Approve call returned.
type approvalOutcome struct {
	approved bool
	err      error
}

// startApprove runs one approval in the background so the test can press a
// button while the broker waits.
func startApprove(ctx context.Context, broker *bridgeAskBroker, question string) chan approvalOutcome {
	out := make(chan approvalOutcome, 1)
	go func() {
		approved, err := broker.Approve(ctx, question)
		out <- approvalOutcome{approved: approved, err: err}
	}()
	return out
}

// waitApprove returns the approval outcome, failing the test when it never
// settles.
func waitApprove(t *testing.T, out chan approvalOutcome) approvalOutcome {
	t.Helper()
	select {
	case got := <-out:
		return got
	case <-time.After(20 * time.Second):
		t.Fatal("Approve never returned")
		return approvalOutcome{}
	}
}

// startAsk runs one ask in the background so the test can press a button while
// the broker waits.
func startAsk(ctx context.Context, broker *bridgeAskBroker, question string, options []string) chan askOutcome {
	out := make(chan askOutcome, 1)
	go func() {
		answer, err := broker.AskOptions(ctx, question, options)
		out <- askOutcome{answer: answer, err: err}
	}()
	return out
}

// waitAsk returns the ask outcome, failing the test when it never settles.
func waitAsk(t *testing.T, out chan askOutcome) askOutcome {
	t.Helper()
	select {
	case got := <-out:
		return got
	case <-time.After(20 * time.Second):
		t.Fatal("AskOptions never returned")
		return askOutcome{}
	}
}

// newTestAskBroker builds a broker whose logger is discarded.
func newTestAskBroker(t *testing.T, tr bridgeAskTransport) *bridgeAskBroker {
	t.Helper()
	return newBridgeAskBroker(tr, testOwnerID, log.New(io.Discard, "", 0))
}

// callbackEvent is the normalized event a button press produces.
func callbackEvent(callbackID, data string) telegram.Event {
	return telegram.Event{Kind: telegram.EventCallback, CallbackID: callbackID, CallbackData: data, ChatID: testOwnerID}
}

// askButtonData returns the callback payload of every button in a rendered
// question, in order.
func askButtonData(t *testing.T, markdown string) []string {
	t.Helper()
	matches := reAskButtonCallback.FindAllStringSubmatch(markdown, -1)
	out := make([]string, 0, len(matches))
	for _, m := range matches {
		out = append(out, m[1])
	}
	return out
}

// randomAskID builds a well-formed question id that belongs to no live question.
func randomAskID(t *testing.T) string {
	t.Helper()
	buf := make([]byte, askIDBytes)
	if _, err := rand.Read(buf); err != nil {
		t.Fatalf("random id: %v", err)
	}
	return hex.EncodeToString(buf)
}

// TestApprovePostsTwoButtonsAndResolvesThePress pins the approval prompt: one
// message, exactly two buttons, an opaque payload and a boolean outcome that
// only the press decides.
func TestApprovePostsTwoButtonsAndResolvesThePress(t *testing.T) {
	for _, tc := range []struct {
		name      string
		press     int
		approved  bool
		wantLabel string
	}{
		{name: "aprobar", press: 0, approved: true, wantLabel: "Aprobar"},
		{name: "rechazar", press: 1, approved: false, wantLabel: "Rechazar"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tr := newRecordingAskTransport()
			broker := newTestAskBroker(t, tr)

			out := startApprove(context.Background(), broker, "¿Ejecutar bash? rm -rf /tmp/x")
			messages := tr.waitMessages(t, 1)
			if len(messages) != 1 {
				t.Fatalf("prompts posted = %d, want exactly one", len(messages))
			}
			markdown := messages[0]
			if !strings.Contains(markdown, "¿Ejecutar bash?") {
				t.Errorf("prompt %q does not carry the question", markdown)
			}
			if !strings.Contains(markdown, ">Aprobar<") || !strings.Contains(markdown, ">Rechazar<") {
				t.Errorf("prompt %q does not carry the two approval buttons", markdown)
			}

			datas := askButtonData(t, markdown)
			if len(datas) != 2 {
				t.Fatalf("buttons = %d (%v), want exactly two", len(datas), datas)
			}
			var questionID string
			for i, data := range datas {
				id, index, ok := parseAskCallback(data)
				if !ok {
					t.Fatalf("button %d payload %q is not a callback this bridge can read", i, data)
				}
				if index != i {
					t.Errorf("button %d payload %q carries index %d", i, data, index)
				}
				if questionID == "" {
					questionID = id
				} else if id != questionID {
					t.Errorf("button %d payload %q belongs to another question", i, data)
				}
				if len(data) > askMaxCallbackBytes {
					t.Errorf("payload %q is %d bytes, over Telegram's %d limit", data, len(data), askMaxCallbackBytes)
				}
				// The payload stays opaque: no question text, no command, no path.
				for _, secret := range []string{"Aprobar", "Rechazar", "bash", "/tmp"} {
					if strings.Contains(data, secret) {
						t.Errorf("payload %q leaks %q", data, secret)
					}
				}
			}

			broker.Resolve(context.Background(), callbackEvent("cb-app", datas[tc.press]))
			got := waitApprove(t, out)
			if got.err != nil {
				t.Fatalf("Approve returned %v, want the press to decide", got.err)
			}
			if got.approved != tc.approved {
				t.Errorf("approved = %v after pressing %q, want %v", got.approved, tc.wantLabel, tc.approved)
			}
			if acks := tr.acknowledgements(); len(acks) != 1 || acks[0] != "cb-app" {
				t.Errorf("acknowledged = %v, want [cb-app] so the button spinner stops", acks)
			}
		})
	}
}

// TestApproveTimeoutFailsClosed proves an unanswered approval is never a silent
// yes: it returns an error and frees the slot so the next question can be asked.
func TestApproveTimeoutFailsClosed(t *testing.T) {
	tr := newRecordingAskTransport()
	broker := newTestAskBroker(t, tr)

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Millisecond)
	defer cancel()

	approved, err := broker.Approve(ctx, "¿Ejecutar write?")
	if err == nil {
		t.Fatal("Approve returned no error after the deadline: an unanswered approval must fail closed")
	}
	if !errors.Is(err, errApprovalNoAnswer) {
		t.Errorf("Approve error = %v, want errApprovalNoAnswer", err)
	}
	if approved {
		t.Error("approved = true with no press: fail-closed forbids it")
	}

	// The slot is free again: the next approval reaches its own deadline instead
	// of failing with the busy error.
	short, cancelShort := context.WithTimeout(context.Background(), 80*time.Millisecond)
	defer cancelShort()
	if _, err := broker.Approve(short, "¿Ejecutar otra vez?"); !errors.Is(err, errApprovalNoAnswer) {
		t.Errorf("second Approve error = %v, want errApprovalNoAnswer: the slot must be free", err)
	}
}

// TestApproveSharesTheSinglePendingSlot documents the chosen design: approvals
// and ask_options questions share one slot, because the limits doc allows the
// owner exactly one pending question. A second caller fails closed instead of
// queueing behind a question the owner may never answer.
func TestApproveSharesTheSinglePendingSlot(t *testing.T) {
	tr := newRecordingAskTransport()
	broker := newTestAskBroker(t, tr)

	approval := startApprove(context.Background(), broker, "¿Ejecutar bash?")
	datas := askButtonData(t, tr.waitMessages(t, 1)[0])

	if _, err := broker.AskOptions(context.Background(), "¿Otra cosa?", []string{"sí", "no"}); !errors.Is(err, errAskBusy) {
		t.Errorf("AskOptions error = %v, want errAskBusy while an approval waits", err)
	}
	if approved, err := broker.Approve(context.Background(), "¿Otra aprobación?"); err == nil || approved {
		t.Errorf("Approve = (%v, %v), want a fail-closed error while an approval waits", approved, err)
	}
	if n := len(tr.messages()); n != 1 {
		t.Errorf("prompts posted = %d, want only the first: a refused prompt must not reach the chat", n)
	}

	// The press still decides the live approval, and the slot is free afterwards.
	broker.Resolve(context.Background(), callbackEvent("cb-1", datas[1]))
	if got := waitApprove(t, approval); got.err != nil || got.approved {
		t.Errorf("Approve = (%v, %v), want a rejection", got.approved, got.err)
	}
	short, cancel := context.WithTimeout(context.Background(), 80*time.Millisecond)
	defer cancel()
	if _, err := broker.AskOptions(short, "¿Ahora sí?", []string{"sí", "no"}); !errors.Is(err, tools.ErrNoAnswer) {
		t.Errorf("AskOptions error = %v, want tools.ErrNoAnswer: the slot must be free again", err)
	}
}

// TestApproveUndeliverablePromptFailsClosed proves an approval that cannot reach
// the chat never becomes an approval: no buttons means no yes.
func TestApproveUndeliverablePromptFailsClosed(t *testing.T) {
	tr := newRecordingAskTransport()
	tr.sendErr = errors.New("telegram: sendRichMessage failed: too many requests")
	broker := newTestAskBroker(t, tr)

	approved, err := broker.Approve(context.Background(), "¿Ejecutar edit?")
	if err == nil {
		t.Fatal("Approve returned no error for an undeliverable prompt")
	}
	if approved {
		t.Error("approved = true although the prompt never reached the chat")
	}
	if acks := tr.acknowledgements(); len(acks) != 0 {
		t.Errorf("acknowledged = %v, want none: no press happened", acks)
	}
}

// TestApproveNeedsAQuestion keeps an empty prompt from posting a button pair with
// nothing to explain.
func TestApproveNeedsAQuestion(t *testing.T) {
	tr := newRecordingAskTransport()
	broker := newTestAskBroker(t, tr)

	if _, err := broker.Approve(context.Background(), "   "); err == nil {
		t.Fatal("Approve accepted an empty question")
	}
	if n := len(tr.messages()); n != 0 {
		t.Errorf("prompts posted = %d, want none", n)
	}
}

// TestAskOptionsPostsOneMessageWithOneButtonPerOption pins the shape of the
// question the owner receives: exactly one message, the question text, one
// button per option with an opaque and non-colliding payload.
func TestAskOptionsPostsOneMessageWithOneButtonPerOption(t *testing.T) {
	for _, count := range []int{2, 3, 4} {
		t.Run(fmt.Sprintf("%d options", count), func(t *testing.T) {
			options := make([]string, count)
			for i := range options {
				options[i] = fmt.Sprintf("opción %d", i+1)
			}
			tr := newRecordingAskTransport()
			broker := newTestAskBroker(t, tr)

			out := startAsk(context.Background(), broker, "¿Cuál elijo?", options)
			messages := tr.waitMessages(t, 1)
			if len(messages) != 1 {
				t.Fatalf("questions posted = %d, want exactly one", len(messages))
			}
			markdown := messages[0]
			if !strings.Contains(markdown, "¿Cuál elijo?") {
				t.Errorf("question %q does not carry the question text", markdown)
			}

			datas := askButtonData(t, markdown)
			if len(datas) != count {
				t.Fatalf("buttons = %d (%v), want one per option (%d)", len(datas), datas, count)
			}
			var questionID string
			seen := map[string]bool{}
			for i, data := range datas {
				id, index, ok := parseAskCallback(data)
				if !ok {
					t.Fatalf("button %d payload %q is not a callback this bridge can read", i, data)
				}
				if index != i {
					t.Errorf("button %d payload %q carries index %d", i, data, index)
				}
				if questionID == "" {
					questionID = id
				} else if id != questionID {
					t.Errorf("button %d payload %q belongs to another question than %q", i, data, questionID)
				}
				if len(data) > askMaxCallbackBytes {
					t.Errorf("payload %q is %d bytes, over Telegram's %d limit", data, len(data), askMaxCallbackBytes)
				}
				if seen[data] {
					t.Errorf("payload %q appears twice: two buttons would press the same option", data)
				}
				seen[data] = true
				// The payload must stay opaque: no label, no question, nothing the
				// owner typed can reach Telegram inside it.
				for _, secret := range append([]string{"opción", "elijo"}, options...) {
					if strings.Contains(data, secret) {
						t.Errorf("payload %q leaks %q", data, secret)
					}
				}
			}
			if !strings.Contains(markdown, ">opción 1<") {
				t.Errorf("question %q does not render the option labels", markdown)
			}

			// The last button must answer, and it must stop the client spinner.
			broker.Resolve(context.Background(), callbackEvent("cb-1", datas[count-1]))
			got := waitAsk(t, out)
			if got.err != nil {
				t.Fatalf("AskOptions returned %v, want the chosen option", got.err)
			}
			if want := options[count-1]; got.answer != want {
				t.Errorf("answer = %q, want %q", got.answer, want)
			}
			if acks := tr.acknowledgements(); len(acks) != 1 || acks[0] != "cb-1" {
				t.Errorf("acknowledged = %v, want [cb-1] so the button spinner stops", acks)
			}
			if n := len(tr.messages()); n != 1 {
				t.Errorf("questions posted = %d after the press, want still one", n)
			}
		})
	}
}

// approvalButtonMarkup is the exact markup the bridge must render for one button:
// its own row, the style only when there is one, and the label as given.
func approvalButtonMarkup(id string, index int, style, label string) string {
	attrs := ""
	if style != "" {
		attrs = ` style="` + style + `"`
	}
	return `<tg-button-row><tg-button type="callback_data"` + attrs + ` data="` +
		askCallbackData(id, index) + `">` + label + `</tg-button></tg-button-row>`
}

// optionsButtonMarkup is the same shape for a neutral ask_options choice, which
// carries no style attribute at all.
func optionsButtonMarkup(id string, index int, label string) string {
	return approvalButtonMarkup(id, index, "", label)
}

// TestAskMarkdownRendersApprovalButtonsStyledAndFullWidth pins the exact markup
// of an approval prompt: one row per button, so each takes the whole width of the
// message on a phone instead of sharing it as a smaller button; the success style
// on the affirmative alone; and the plain labels, unchanged.
func TestAskMarkdownRendersApprovalButtonsStyledAndFullWidth(t *testing.T) {
	id := randomAskID(t)

	got := askMarkdown("¿Ejecutar bash?", approvalOptions, id, askKindApproval)
	want := "¿Ejecutar bash?\n\n" +
		approvalButtonMarkup(id, 0, "success", "Aprobar") + "\n" +
		approvalButtonMarkup(id, 1, "", "Rechazar")
	if got != want {
		t.Fatalf("approval markdown =\n%s\nwant\n%s", got, want)
	}
	if rows := strings.Count(got, "<tg-button-row>"); rows != len(approvalOptions) {
		t.Errorf("rows = %d in %q, want one per approval button (%d)", rows, got, len(approvalOptions))
	}
	// Exactly one style, on the affirmative: the negative keeps the default look.
	if styles := strings.Count(got, `style="`); styles != 1 {
		t.Errorf("styled buttons = %d in %q, want only the affirmative", styles, got)
	}
	if !strings.Contains(got, `<tg-button type="callback_data" data="`+askCallbackData(id, 1)+`">Rechazar</tg-button>`) {
		t.Errorf("markdown %q does not render Rechazar with no style attribute", got)
	}
	for _, unwanted := range []string{"danger", "primary", "✅", "❌"} {
		if strings.Contains(got, unwanted) {
			t.Errorf("approval markdown %q carries %q", got, unwanted)
		}
	}
}

// TestAskMarkdownRendersOptionsButtonsStyledAndFullWidth pins the exact markup of
// an ask_options question: one row per option, no style attribute anywhere, and
// the option labels exactly as the model wrote them.
func TestAskMarkdownRendersOptionsButtonsStyledAndFullWidth(t *testing.T) {
	id := randomAskID(t)

	for _, options := range [][]string{
		{"guardar", "resumir"},
		{"opción 1", "opción 2", "opción 3"},
		{"uno", "dos", "tres", "cuatro"},
	} {
		t.Run(fmt.Sprintf("%d options", len(options)), func(t *testing.T) {
			var want strings.Builder
			want.WriteString("¿Qué hago con el enlace?\n\n")
			for i, option := range options {
				if i > 0 {
					want.WriteString("\n")
				}
				want.WriteString(optionsButtonMarkup(id, i, option))
			}

			got := askMarkdown("¿Qué hago con el enlace?", options, id, askKindOptions)
			if got != want.String() {
				t.Fatalf("options markdown =\n%s\nwant\n%s", got, want.String())
			}
			if rows := strings.Count(got, "<tg-button-row>"); rows != len(options) {
				t.Errorf("rows = %d in %q, want one per option (%d)", rows, got, len(options))
			}
			if strings.Contains(got, `style="`) {
				t.Errorf("options markdown %q styles a neutral choice", got)
			}
		})
	}
}

// TestAskMarkdownStyledButtonsKeepTheCallbackPayload proves the restyling is
// presentation only: every rendered payload is byte-identical to the format the
// bridge produced before, so the unchanged parse/resolve/acknowledge path still
// resolves the press.
func TestAskMarkdownStyledButtonsKeepTheCallbackPayload(t *testing.T) {
	// The payload is the question id and the option index, and nothing else: no
	// style, no label, no mark.
	format := regexp.MustCompile(`^ask:[0-9a-f]{16}:[0-9]$`)

	tr := newRecordingAskTransport()
	broker := newTestAskBroker(t, tr)

	out := startApprove(context.Background(), broker, "¿Ejecutar bash?")
	markdown := tr.waitMessages(t, 1)[0]

	datas := askButtonData(t, markdown)
	if len(datas) != len(approvalOptions) {
		t.Fatalf("buttons = %d (%v), want %d", len(datas), datas, len(approvalOptions))
	}
	for i, data := range datas {
		if !format.MatchString(data) {
			t.Fatalf("payload %q is not the ask:<id>:<index> shape the bridge always produced", data)
		}
		id, index, ok := parseAskCallback(data)
		if !ok || index != i {
			t.Fatalf("payload %q parses to (%q, %d, %v), want index %d", data, id, index, ok, i)
		}
		if want := askCallbackData(id, i); data != want {
			t.Errorf("payload = %q, want %q byte for byte", data, want)
		}
		if want := "ask:" + id + ":" + strconv.Itoa(i); data != want {
			t.Errorf("payload = %q, want the unchanged %q", data, want)
		}
	}

	// The styled button's payload is the one that resolves the approval, and the
	// press is still acknowledged.
	broker.Resolve(context.Background(), callbackEvent("cb-styled", datas[approvalIndexAllow]))
	got := waitApprove(t, out)
	if got.err != nil || !got.approved {
		t.Errorf("Approve = (%v, %v), want the styled button to approve", got.approved, got.err)
	}
	if acks := tr.acknowledgements(); len(acks) != 1 || acks[0] != "cb-styled" {
		t.Errorf("acknowledged = %v, want [cb-styled] so the button spinner stops", acks)
	}
}

// TestAskOptionsUnknownCallbackIsAcknowledgedAndIgnored covers the presses that
// settle nothing: a foreign payload, garbage, an out-of-range index and a press
// arriving with no question pending. All of them stop the spinner and none of
// them answers a question.
func TestAskOptionsUnknownCallbackIsAcknowledgedAndIgnored(t *testing.T) {
	t.Run("nothing pending", func(t *testing.T) {
		tr := newRecordingAskTransport()
		broker := newTestAskBroker(t, tr)

		// A press from an old message must not panic or hang the poll loop.
		broker.Resolve(context.Background(), callbackEvent("cb-idle", askCallbackData(randomAskID(t), 0)))
		if acks := tr.acknowledgements(); len(acks) != 1 || acks[0] != "cb-idle" {
			t.Errorf("acknowledged = %v, want the stale press acknowledged", acks)
		}
	})

	t.Run("while a question waits", func(t *testing.T) {
		tr := newRecordingAskTransport()
		broker := newTestAskBroker(t, tr)

		out := startAsk(context.Background(), broker, "¿Guardo el enlace?", []string{"guardar", "descartar"})
		datas := askButtonData(t, tr.waitMessages(t, 1)[0])
		id, _, ok := parseAskCallback(datas[0])
		if !ok {
			t.Fatalf("payload %q is not readable", datas[0])
		}

		foreign := []struct{ callbackID, data string }{
			{"cb-foreign", askCallbackData(randomAskID(t), 0)},
			{"cb-garbage", "otra-cosa"},
			{"cb-range", askCallbackData(id, 9)},
		}
		for _, f := range foreign {
			broker.Resolve(context.Background(), callbackEvent(f.callbackID, f.data))
		}
		if acks := tr.acknowledgements(); len(acks) != len(foreign) {
			t.Errorf("acknowledged = %v, want every press acknowledged (%d)", acks, len(foreign))
		}

		// The pending question survived: the slot is still taken.
		if _, err := broker.AskOptions(context.Background(), "otra", []string{"a", "b"}); !errors.Is(err, errAskBusy) {
			t.Errorf("second ask error = %v, want errAskBusy: the unknown presses must not settle the question", err)
		}

		// And the real press still answers.
		broker.Resolve(context.Background(), callbackEvent("cb-real", datas[0]))
		got := waitAsk(t, out)
		if got.err != nil || got.answer != "guardar" {
			t.Errorf("answer = %q (err %v), want guardar", got.answer, got.err)
		}
	})
}

// TestResolveAcknowledgesThePressWithAToast pins the visible feedback of a
// press: the acknowledgement that already stopped the spinner now carries a
// short Spanish toast saying what the press did, and a press that settles nothing
// stays silent. One press is acknowledged exactly once either way.
func TestResolveAcknowledgesThePressWithAToast(t *testing.T) {
	t.Run("aprobar", func(t *testing.T) {
		tr := newRecordingAskTransport()
		broker := newTestAskBroker(t, tr)

		out := startApprove(context.Background(), broker, "¿Ejecutar bash? rm -rf /tmp/x")
		datas := askButtonData(t, tr.waitMessages(t, 1)[0])

		broker.Resolve(context.Background(), callbackEvent("cb-allow", datas[approvalIndexAllow]))
		if got := waitApprove(t, out); got.err != nil || !got.approved {
			t.Fatalf("Approve = (%v, %v), want an approval", got.approved, got.err)
		}
		assertAskAckToast(t, tr, "cb-allow", "Aprobado")
	})

	t.Run("rechazar", func(t *testing.T) {
		tr := newRecordingAskTransport()
		broker := newTestAskBroker(t, tr)

		out := startApprove(context.Background(), broker, "¿Ejecutar bash? rm -rf /tmp/x")
		datas := askButtonData(t, tr.waitMessages(t, 1)[0])

		broker.Resolve(context.Background(), callbackEvent("cb-deny", datas[approvalIndexAllow+1]))
		if got := waitApprove(t, out); got.err != nil || got.approved {
			t.Fatalf("Approve = (%v, %v), want a rejection", got.approved, got.err)
		}
		assertAskAckToast(t, tr, "cb-deny", "Rechazado")
	})

	t.Run("opción", func(t *testing.T) {
		tr := newRecordingAskTransport()
		broker := newTestAskBroker(t, tr)

		out := startAsk(context.Background(), broker, "¿Qué hago con el enlace?", []string{"guardar", "resumir"})
		datas := askButtonData(t, tr.waitMessages(t, 1)[0])

		broker.Resolve(context.Background(), callbackEvent("cb-opt", datas[1]))
		if got := waitAsk(t, out); got.err != nil || got.answer != "resumir" {
			t.Fatalf("answer = %q (err %v), want resumir", got.answer, got.err)
		}
		assertAskAckToast(t, tr, "cb-opt", "resumir")
	})

	t.Run("opción larga", func(t *testing.T) {
		tr := newRecordingAskTransport()
		broker := newTestAskBroker(t, tr)

		// Longer than a toast should be, so the acknowledgement has to cut it.
		long := strings.Repeat("sí ", 20)
		out := startAsk(context.Background(), broker, "¿Qué hago?", []string{"guardar", long})
		datas := askButtonData(t, tr.waitMessages(t, 1)[0])

		broker.Resolve(context.Background(), callbackEvent("cb-long", datas[1]))
		// The answer itself is never truncated.
		if got := waitAsk(t, out); got.err != nil || got.answer != long {
			t.Fatalf("answer = %q (err %v), want the full label", got.answer, got.err)
		}
		want := string([]rune(long)[:askAckMaxRunes]) + "…"
		assertAskAckToast(t, tr, "cb-long", want)
	})

	t.Run("obsoleta", func(t *testing.T) {
		tr := newRecordingAskTransport()
		broker := newTestAskBroker(t, tr)

		// A button from an old message: the spinner still stops, but there is
		// nothing to confirm, so the acknowledgement carries no text.
		broker.Resolve(context.Background(), callbackEvent("cb-stale", askCallbackData(randomAskID(t), 0)))
		assertAskAckToast(t, tr, "cb-stale", "")
	})
}

// TestAskOptionsSecondConcurrentAskFailsFast documents the chosen behaviour for
// overlapping questions: the second call gets a clear error instead of queueing,
// because the owner can only answer the question they are looking at.
func TestAskOptionsSecondConcurrentAskFailsFast(t *testing.T) {
	tr := newRecordingAskTransport()
	broker := newTestAskBroker(t, tr)

	out := startAsk(context.Background(), broker, "¿Primera?", []string{"uno", "dos"})
	tr.waitMessages(t, 1)

	_, err := broker.AskOptions(context.Background(), "¿Segunda?", []string{"tres", "cuatro"})
	if !errors.Is(err, errAskBusy) {
		t.Fatalf("second ask error = %v, want errAskBusy", err)
	}
	if n := len(tr.messages()); n != 1 {
		t.Errorf("questions posted = %d, want only the first: a refused ask must not reach the chat", n)
	}

	datas := askButtonData(t, tr.messages()[0])
	broker.Resolve(context.Background(), callbackEvent("cb-1", datas[1]))
	if got := waitAsk(t, out); got.err != nil || got.answer != "dos" {
		t.Errorf("answer = %q (err %v), want dos", got.answer, got.err)
	}

	// The slot is free again, so the refused question can be asked later: the
	// only error it can return is its own deadline, never the busy one.
	short, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if _, err := broker.AskOptions(short, "¿Segunda?", []string{"tres", "cuatro"}); !errors.Is(err, tools.ErrNoAnswer) {
		t.Errorf("later ask error = %v, want tools.ErrNoAnswer: the slot must be free again", err)
	}
}

// TestAskOptionsSequentialQuestionsDoNotCross proves two questions never share
// an identity: a late press of the first question is acknowledged but cannot
// answer the second, which only its own button settles.
func TestAskOptionsSequentialQuestionsDoNotCross(t *testing.T) {
	tr := newRecordingAskTransport()
	broker := newTestAskBroker(t, tr)

	first := startAsk(context.Background(), broker, "¿Primera?", []string{"uno", "dos"})
	firstData := askButtonData(t, tr.waitMessages(t, 1)[0])
	broker.Resolve(context.Background(), callbackEvent("cb-1", firstData[0]))
	if got := waitAsk(t, first); got.err != nil || got.answer != "uno" {
		t.Fatalf("first answer = %q (err %v), want uno", got.answer, got.err)
	}

	second := startAsk(context.Background(), broker, "¿Segunda?", []string{"tres", "cuatro"})
	questions := tr.waitMessages(t, 2)
	secondData := askButtonData(t, questions[1])
	if len(secondData) != 2 {
		t.Fatalf("second question buttons = %v, want 2", secondData)
	}
	firstID, _, _ := parseAskCallback(firstData[0])
	secondID, _, _ := parseAskCallback(secondData[0])
	if firstID == secondID {
		t.Fatalf("both questions used the id %q: a stale press could answer the wrong question", firstID)
	}

	// The first question's button pressed late: acknowledged, ignored.
	broker.Resolve(context.Background(), callbackEvent("cb-late", firstData[1]))
	if acks := tr.acknowledgements(); acks[len(acks)-1] != "cb-late" {
		t.Errorf("acknowledged = %v, want the late press acknowledged", acks)
	}
	if _, err := broker.AskOptions(context.Background(), "¿Tercera?", []string{"a", "b"}); !errors.Is(err, errAskBusy) {
		t.Errorf("error = %v, want errAskBusy: the late press must not settle the second question", err)
	}

	// Only its own button answers it.
	broker.Resolve(context.Background(), callbackEvent("cb-2", secondData[1]))
	if got := waitAsk(t, second); got.err != nil || got.answer != "cuatro" {
		t.Errorf("second answer = %q (err %v), want cuatro", got.answer, got.err)
	}
}

// TestAskOptionsCancellationLeavesNothingBehind proves a cancelled wait returns
// the "sin respuesta" path, leaves no goroutine and frees the slot, so a press
// from the cancelled question can never resolve a later one.
func TestAskOptionsCancellationLeavesNothingBehind(t *testing.T) {
	tr := newRecordingAskTransport()
	broker := newTestAskBroker(t, tr)

	before := runtime.NumGoroutine()
	ctx, cancel := context.WithCancel(context.Background())
	out := startAsk(ctx, broker, "¿Sigo?", []string{"sí", "no"})
	cancelledData := askButtonData(t, tr.waitMessages(t, 1)[0])

	cancel()
	got := waitAsk(t, out)
	if !errors.Is(got.err, tools.ErrNoAnswer) {
		t.Errorf("AskOptions error = %v, want tools.ErrNoAnswer (the \"sin respuesta\" result)", got.err)
	}
	if got.answer != "" {
		t.Errorf("answer = %q, want none", got.answer)
	}
	assertNoGoroutineGrowth(t, before)

	// The pending entry is gone: a press from the cancelled question is
	// acknowledged and settles nothing.
	next := startAsk(context.Background(), broker, "¿Otra?", []string{"tres", "cuatro"})
	tr.waitMessages(t, 2)
	broker.Resolve(context.Background(), callbackEvent("cb-stale", cancelledData[0]))
	if _, err := broker.AskOptions(context.Background(), "¿Tercera?", []string{"a", "b"}); !errors.Is(err, errAskBusy) {
		t.Errorf("error = %v, want errAskBusy: the cancelled question must not resolve the next one", err)
	}

	nextData := askButtonData(t, tr.messages()[1])
	broker.Resolve(context.Background(), callbackEvent("cb-next", nextData[0]))
	if got := waitAsk(t, next); got.err != nil || got.answer != "tres" {
		t.Errorf("next answer = %q (err %v), want tres", got.answer, got.err)
	}
}

// TestAskOptionsExpiryAnswersSinRespuesta drives the real ask_options route with
// a short deadline: an unanswered question is a normal 200 with the "sin
// respuesta" answer, never an error the model has to explain.
func TestAskOptionsExpiryAnswersSinRespuesta(t *testing.T) {
	tr := newRecordingAskTransport()
	broker := newTestAskBroker(t, tr)

	server, err := tools.New(tools.Config{
		Vault:      vault.New(t.TempDir(), 0),
		Files:      askNoopFileSender{},
		Asker:      broker,
		AskTimeout: 150 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("tools.New: %v", err)
	}
	base, err := server.Start()
	if err != nil {
		t.Fatalf("tools server start: %v", err)
	}
	t.Cleanup(func() { _ = server.Close() })

	resp := postAskOptions(context.Background(), base+"/tools/ask_options", server.Token(), "¿Sigo?", []string{"sí", "no"})
	if resp.Status != http.StatusOK {
		t.Fatalf("status = %d (%s), want 200 with the sin respuesta answer", resp.Status, resp.Error)
	}
	if resp.Answer != "sin respuesta" || !resp.TimedOut {
		t.Errorf("answer = %+v, want the \"sin respuesta\" result", resp)
	}

	messages := tr.waitMessages(t, 1)
	datas := askButtonData(t, messages[0])

	// A press arriving after the deadline is acknowledged and settles nothing,
	// and the slot is free for the next question: the only error it can return is
	// its own deadline, never the busy one.
	broker.Resolve(context.Background(), callbackEvent("cb-late", datas[0]))
	short, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if _, err := broker.AskOptions(short, "¿Otra?", []string{"a", "b"}); !errors.Is(err, tools.ErrNoAnswer) {
		t.Errorf("new ask error = %v, want tools.ErrNoAnswer: the slot must be free after expiry", err)
	}
}

// fakeAskBot is the base fake Bot API plus the two routes inline buttons need.
// sendRichMessage answers with a proper envelope — the base fake records the
// message but writes no body, which would turn every question into a decode
// error — and answerCallbackQuery records the acknowledgement.
type fakeAskBot struct {
	*fakeBot
	ackMu sync.Mutex
	acks  []string
}

// newFakeAskBot starts the ask Bot API. Everything but the two ask routes is
// delegated to the base fake, so polling is recorded exactly as in the text
// slice.
func newFakeAskBot(t *testing.T) (*fakeAskBot, *httptest.Server) {
	t.Helper()
	b := &fakeAskBot{fakeBot: &fakeBot{delivered: make(chan struct{}, 8)}}
	srv := httptest.NewServer(http.HandlerFunc(b.route))
	t.Cleanup(srv.Close)
	return b, srv
}

// route serves the ask routes and delegates the rest to the base fake.
func (b *fakeAskBot) route(w http.ResponseWriter, r *http.Request) {
	switch {
	case strings.HasSuffix(r.URL.Path, "/sendRichMessage"):
		var payload struct {
			ChatID      int64 `json:"chat_id"`
			RichMessage struct {
				Markdown string `json:"markdown"`
			} `json:"rich_message"`
		}
		decodeBotJSON(r, &payload)
		b.fakeBot.mu.Lock()
		b.finals = append(b.finals, payload.RichMessage.Markdown)
		b.fakeBot.mu.Unlock()
		writeBotJSON(w, map[string]any{"ok": true, "result": map[string]any{
			"message_id": 1,
			"chat":       map[string]any{"id": payload.ChatID},
		}})
		b.signal()

	case strings.HasSuffix(r.URL.Path, "/answerCallbackQuery"):
		var payload struct {
			CallbackQueryID string `json:"callback_query_id"`
		}
		decodeBotJSON(r, &payload)
		b.ackMu.Lock()
		b.acks = append(b.acks, payload.CallbackQueryID)
		b.ackMu.Unlock()
		writeBotJSON(w, map[string]any{"ok": true, "result": true})

	default:
		b.fakeBot.handle(w, r)
	}
}

// acknowledgements lists the callback queries the bridge acknowledged, in order.
func (b *fakeAskBot) acknowledgements() []string {
	b.ackMu.Lock()
	defer b.ackMu.Unlock()
	return append([]string(nil), b.acks...)
}

// askHarness is one ask wiring under test.
type askHarness struct {
	runtime *bridgeRuntime
	bot     *fakeAskBot
	spawns  *argsRecorder
	cancel  context.CancelFunc
	finish  chan struct{}
	runErr  error
}

// startAskBridge composes the real wiring against the ask fake and runs it, so
// the whole route — tools endpoint, poller and broker — is exercised.
func startAskBridge(t *testing.T) *askHarness {
	t.Helper()

	bot, srv := newFakeAskBot(t)
	spawns := &argsRecorder{}
	t.Setenv(envFakePiScenario, "stream")

	cfg := config.Config{
		TelegramToken:   testBotToken,
		TelegramChatID:  testOwnerID,
		TelegramAPIBase: srv.URL,
		VaultDir:        t.TempDir(),
		BridgeSessionID: "ask-session",
		BridgeTools:     "read,grep,find,ls,save_note,append_note,send_file,ask_options",
		BridgeTimeout:   20 * time.Second,
		PiBin:           "pi",
	}

	rt, err := newBridgeRuntime(cfg, log.New(io.Discard, "", 0),
		withBridgeCommandFactory(fakePiFactory(spawns)))
	if err != nil {
		t.Fatalf("newBridgeRuntime: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	h := &askHarness{runtime: rt, bot: bot, spawns: spawns, cancel: cancel, finish: make(chan struct{})}
	go func() {
		h.runErr = rt.Run(ctx)
		close(h.finish)
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-h.finish:
		case <-time.After(20 * time.Second):
		}
	})
	return h
}

func (h *askHarness) stop(t *testing.T) error {
	t.Helper()
	h.cancel()
	select {
	case <-h.finish:
		return h.runErr
	case <-time.After(20 * time.Second):
		t.Fatal("Run did not return after cancellation")
		return nil
	}
}

// TestBridgeCallbackResolvesAskOptionsWithoutReachingPi runs the real wiring:
// the tools route posts the question, the poller receives the button press, the
// broker answers ask_options and Pi is never invoked with the press.
func TestBridgeCallbackResolvesAskOptionsWithoutReachingPi(t *testing.T) {
	h := startAskBridge(t)

	responses := make(chan askHTTPResponse, 1)
	go func() {
		responses <- postAskOptions(context.Background(),
			h.runtime.toolsURL+"/tools/ask_options", h.runtime.tools.Token(),
			"¿Qué hago con el enlace?", []string{"guardar", "resumir"})
	}()

	// The question itself is a rich message; its buttons carry the payload the
	// owner's client will send back.
	h.bot.waitDelivery(t)
	finals, messages, _, _ := h.bot.snapshot()
	if len(finals) != 1 || len(messages) != 0 {
		t.Fatalf("question deliveries = %v (classic %v), want one rich message", finals, messages)
	}
	datas := askButtonData(t, finals[0])
	if len(datas) != 2 {
		t.Fatalf("buttons = %v, want 2", datas)
	}

	pushUpdates(h.bot.fakeBot, callbackUpdate(901, "cb-42", datas[1]))

	var resp askHTTPResponse
	select {
	case resp = <-responses:
	case <-time.After(20 * time.Second):
		t.Fatal("ask_options never returned after the button press")
	}
	if resp.Status != http.StatusOK || resp.Answer != "resumir" || resp.TimedOut {
		t.Errorf("ask_options = %+v, want the pressed option resumir", resp)
	}
	// The press must be acknowledged so the client spinner stops.
	if acks := h.bot.acknowledgements(); len(acks) != 1 || acks[0] != "cb-42" {
		t.Errorf("acknowledged = %v, want [cb-42]", acks)
	}

	// A press is not free text: Pi must not have been woken at all.
	if args := h.spawns.first(); args != nil {
		t.Errorf("Pi was spawned with %v: a button press must never reach it as a prompt", args)
	}

	if err := h.stop(t); !errors.Is(err, context.Canceled) {
		t.Errorf("Run returned %v, want context.Canceled", err)
	}
}

// TestAskOptionsSendFailureIsReported proves a question that cannot be delivered
// fails loudly instead of waiting for a press that has no button to come from.
func TestAskOptionsSendFailureIsReported(t *testing.T) {
	tr := newRecordingAskTransport()
	tr.sendErr = errors.New("telegram: sendRichMessage failed: too many buttons")
	broker := newTestAskBroker(t, tr)

	if _, err := broker.AskOptions(context.Background(), "¿Cuál?", []string{"uno", "dos"}); err == nil {
		t.Fatal("AskOptions returned no error for an undeliverable question")
	}
	// The failed ask must not hold the slot: once delivery works again the next
	// call reaches its own deadline instead of failing fast with the busy error.
	tr.mu.Lock()
	tr.sendErr = nil
	tr.mu.Unlock()
	short, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if _, err := broker.AskOptions(short, "¿Cuál?", []string{"uno", "dos"}); !errors.Is(err, tools.ErrNoAnswer) {
		t.Errorf("retry error = %v, want tools.ErrNoAnswer: a failed send must free the slot", err)
	}
	if acks := tr.acknowledgements(); len(acks) != 0 {
		t.Errorf("acknowledged = %v, want none: no press happened", acks)
	}
}

// TestAskOptionsKeepsItsErrorText pins the ask_options errors that T13 refactored
// into the shared ask helper. The text the model already received must not change
// just because an approval now shares the code.
func TestAskOptionsKeepsItsErrorText(t *testing.T) {
	tr := newRecordingAskTransport()
	tr.sendErr = errors.New("telegram: sendRichMessage failed")
	broker := newTestAskBroker(t, tr)

	_, err := broker.AskOptions(context.Background(), "¿Cuál?", []string{"uno", "dos"})
	if err == nil {
		t.Fatal("AskOptions returned no error for an undeliverable question")
	}
	if !strings.HasPrefix(err.Error(), "ask_options: send question: ") {
		t.Errorf("AskOptions error = %q, want the ask_options send-failure text", err)
	}
	if !strings.Contains(err.Error(), "telegram: sendRichMessage failed") {
		t.Errorf("error %q does not carry the cause", err)
	}

	// The two outcomes the tools server distinguishes keep their identity.
	tr.mu.Lock()
	tr.sendErr = nil
	tr.mu.Unlock()
	busy := startAsk(context.Background(), broker, "¿Primera?", []string{"uno", "dos"})
	tr.waitMessages(t, 1)
	if _, err := broker.AskOptions(context.Background(), "¿Segunda?", []string{"tres", "cuatro"}); !errors.Is(err, errAskBusy) {
		t.Errorf("second ask error = %q, want errAskBusy itself, not a wrapped error", err)
	}
	broker.Resolve(context.Background(), callbackEvent("cb-1", askButtonData(t, tr.messages()[0])[1]))
	if got := waitAsk(t, busy); got.err != nil || got.answer != "dos" {
		t.Errorf("answer = %q (err %v), want dos", got.answer, got.err)
	}
}

// askHTTPResponse is one decoded answer from the ask_options route.
type askHTTPResponse struct {
	Status   int
	Answer   string
	TimedOut bool
	Error    string
}

// postAskOptions calls the real ask_options route and decodes its answer.
func postAskOptions(ctx context.Context, url, token, question string, options []string) askHTTPResponse {
	body, err := json.Marshal(map[string]any{"question": question, "options": options})
	if err != nil {
		return askHTTPResponse{Error: err.Error()}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return askHTTPResponse{Error: err.Error()}
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Alter-Token", token)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return askHTTPResponse{Error: err.Error()}
	}
	defer resp.Body.Close()

	raw, _ := io.ReadAll(resp.Body)
	var out struct {
		Answer   string `json:"answer"`
		TimedOut bool   `json:"timed_out"`
		Error    string `json:"error"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return askHTTPResponse{Status: resp.StatusCode, Error: fmt.Sprintf("decode %s: %v", raw, err)}
	}
	return askHTTPResponse{Status: resp.StatusCode, Answer: out.Answer, TimedOut: out.TimedOut, Error: out.Error}
}

// callbackUpdate is the Telegram update a button press produces.
func callbackUpdate(updateID int, callbackID, data string) telegram.Update {
	return telegram.Update{
		ID: updateID,
		CallbackQuery: &telegram.CallbackQuery{
			ID:   callbackID,
			Data: data,
			From: &telegram.User{ID: testOwnerID},
			Message: &telegram.Message{
				Chat: telegram.Chat{ID: testOwnerID},
			},
		},
	}
}

// pushUpdates queues updates for the next poll, which is how a test presses a
// button while the wiring is already running.
func pushUpdates(b *fakeBot, updates ...telegram.Update) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.batches = append(b.batches, updates)
}

// assertNoGoroutineGrowth fails when a finished wait left a goroutine behind.
func assertNoGoroutineGrowth(t *testing.T, before int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if runtime.NumGoroutine() <= before {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Errorf("goroutines went from %d to %d: the cancelled question left work behind", before, runtime.NumGoroutine())
}

// askNoopFileSender satisfies tools.FileSender for the route tests, which never
// send a file.
type askNoopFileSender struct{}

func (askNoopFileSender) SendFile(context.Context, string, string) error { return nil }
