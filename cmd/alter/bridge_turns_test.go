package main

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

// The turn dispatch of T13/L15 has two structural guarantees that no chat-level
// assertion pins on its own: Run must not tear the runtime down while a turn is
// still running (turns.Wait), and two queued messages must not interleave
// (turnMu). Both are exercised here through the observable surfaces — the fake
// Bot API, the captured log and the turn slot itself — plus the turn seam, which
// is the only way to hold a turn open after Run's context is cancelled.

// TestApprovalPressFromAnEarlierQuestionIsIgnored proves a stale button cannot
// settle a newer approval: the pending question's own id is what admits a press,
// and a press for any other question is acknowledged and dropped. Covers L15.
func TestApprovalPressFromAnEarlierQuestionIsIgnored(t *testing.T) {
	tr := newRecordingAskTransport()
	broker := newTestAskBroker(t, tr)

	stale := startApprove(context.Background(), broker, "¿Ejecutar bash?")
	staleButtons := askButtonData(t, tr.waitMessages(t, 1)[0])
	broker.Resolve(context.Background(), callbackEvent("cb-old", staleButtons[1]))
	if got := waitApprove(t, stale); got.err != nil || got.approved {
		t.Fatalf("first approval = (%v, %v), want a rejection", got.approved, got.err)
	}

	live := startApprove(context.Background(), broker, "¿Ejecutar write?")
	if len(tr.waitMessages(t, 2)) != 2 {
		t.Fatalf("second approval was never posted")
	}
	// The old "Aprobar" button pressed again: same shape, dead question.
	broker.Resolve(context.Background(), callbackEvent("cb-old-again", staleButtons[0]))

	select {
	case got := <-live:
		t.Fatalf("stale press settled the live approval: approved=%v err=%v", got.approved, got.err)
	case <-time.After(200 * time.Millisecond):
	}

	// The live question still belongs to the owner: its own press decides it.
	liveButtons := askButtonData(t, tr.waitMessages(t, 2)[1])
	broker.Resolve(context.Background(), callbackEvent("cb-live", liveButtons[0]))
	if got := waitApprove(t, live); got.err != nil || !got.approved {
		t.Fatalf("live approval = (%v, %v), want an approval", got.approved, got.err)
	}
}

// TestShutdownWithPendingApprovalFailsClosed proves cancellation ends a turn that
// is blocked on a button press: Run returns, the approval fails closed, and the
// turn is already finished when Run returns (turns.Wait is load-bearing).
// Covers L15.
func TestShutdownWithPendingApprovalFailsClosed(t *testing.T) {
	h := startDialogBridge(t, "dialog-confirm", 120*time.Second)
	h.sendText(1, 1, "borra el log")
	if q := h.waitRich(t, 1)[0]; !strings.Contains(q, "Aprobar") {
		t.Fatalf("question %q was not posted with the approval buttons", q)
	}
	// The fake records the question before it answers the POST, so give the broker
	// a moment to finish sending and park on the button: cancelling mid-POST fails
	// the approval at the send, not at the unanswered press this test pins.
	time.Sleep(100 * time.Millisecond)

	start := time.Now()
	if err := h.stop(t); !errors.Is(err, context.Canceled) {
		t.Fatalf("Run returned %v, want context.Canceled", err)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Errorf("Run took %s to return with a pending approval: shutdown waits for the button", elapsed)
	}
	logs := h.logs.String()
	if !strings.Contains(logs, "did not answer, so the action is blocked") {
		t.Errorf("log %q does not report the blocked approval", logs)
	}
	// The turn goroutine is gone before teardown finished: otherwise the reply and
	// the log above could still be running against a closed session.
	if !h.runtime.turnMu.TryLock() {
		t.Error("a turn was still running when Run returned")
	}
}

// TestQueuedTurnsRunOnceInOrder proves the two structural guarantees of L15 at
// once: a press is consumed while a turn is in flight (turn one settles), and the
// queued turn runs exactly once, in order, after it (turnMu serializes).
func TestQueuedTurnsRunOnceInOrder(t *testing.T) {
	h := startDialogBridge(t, "dialog-confirm", 120*time.Second)
	h.sendText(1, 1, "uno")
	h.sendText(2, 2, "dos")

	q1 := h.waitRich(t, 1)[0]
	pushUpdates(h.bot.fakeBot, callbackUpdate(3, "cb-1", askButtonData(t, q1)[0]))

	three := h.waitRich(t, 3)
	if len(three) != 3 {
		t.Fatalf("deliveries = %d (%v), want question, answer, question", len(three), three)
	}
	if !strings.HasPrefix(three[1], `{"confirmed":true`) {
		t.Errorf("delivery[1] = %q, want the answer to the first approval", three[1])
	}
	if !strings.Contains(three[2], "Aprobar") {
		t.Errorf("delivery[2] = %q, want the second turn's question after the first turn finished", three[2])
	}
	pushUpdates(h.bot.fakeBot, callbackUpdate(4, "cb-2", askButtonData(t, three[2])[0]))

	four := h.waitRich(t, 4)
	if !strings.HasPrefix(four[3], `{"confirmed":true`) {
		t.Errorf("delivery[3] = %q, want the second answer", four[3])
	}
	time.Sleep(300 * time.Millisecond)
	if got := len(h.rich()); got != 4 {
		t.Errorf("deliveries after settling = %d (%v), want exactly 4: no turn lost or repeated", got, h.rich())
	}
	if err := h.stop(t); !errors.Is(err, context.Canceled) {
		t.Errorf("Run returned %v, want context.Canceled", err)
	}
}

// TestRunWaitsForAnInFlightTurn pins turns.Wait: Run must not return — and so must
// not close the tools server and the Pi session — while a turn started by the poll
// loop is still running. The turn seam holds the turn open after the runtime's
// context is cancelled, which is exactly the window teardown must wait for;
// without turns.Wait, Run returns from under the live turn.
func TestRunWaitsForAnInFlightTurn(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	releaseOnce := sync.OnceFunc(func() { close(release) })
	defer releaseOnce()

	h := startDialogBridge(t, "dialog-confirm", 0, withTurnGate(func(context.Context) {
		close(entered)
		<-release
	}))
	h.sendText(1, 1, "borra el log")

	select {
	case <-entered:
	case <-time.After(20 * time.Second):
		t.Fatal("the turn never started: the seam was not reached")
	}

	h.cancel()
	select {
	case <-h.finish:
		t.Fatal("Run returned while a turn was still in flight: teardown took the session and tools server away from a live turn")
	case <-time.After(300 * time.Millisecond):
	}

	releaseOnce()
	select {
	case <-h.finish:
	case <-time.After(20 * time.Second):
		t.Fatal("Run did not return after the in-flight turn finished")
	}
	if !errors.Is(h.runErr, context.Canceled) {
		t.Errorf("Run returned %v, want context.Canceled", h.runErr)
	}
	if !h.runtime.turnMu.TryLock() {
		t.Error("a turn was still running when Run returned")
	}
}

// TestQueuedTurnWaitsForTheInFlightTurn pins turnMu: a message that arrives while
// another turn is running must wait for it instead of interleaving. Turn one is
// parked on the approval, which the test controls, so the queued turn must show no
// sign of life — not even its typing action — until turn one has answered.
func TestQueuedTurnWaitsForTheInFlightTurn(t *testing.T) {
	h := startDialogBridge(t, "dialog-confirm", 120*time.Second)
	h.sendText(1, 1, "uno")

	q1 := h.waitRich(t, 1)[0]
	if !strings.Contains(q1, "Aprobar") {
		t.Fatalf("first question = %q, want the approval buttons", q1)
	}

	// Turn one is parked on the button, so it still owns the turn slot.
	h.sendText(2, 2, "dos")
	deadline := time.Now().Add(300 * time.Millisecond)
	for time.Now().Before(deadline) {
		if _, _, _, typing := h.bot.snapshot(); len(typing) > 1 {
			t.Fatalf("typing actions = %d: the queued turn started while the first one was still in flight", len(typing))
		}
		if len(h.rich()) != 1 {
			t.Fatalf("deliveries = %v: the queued turn delivered while the first one was still in flight", h.rich())
		}
		time.Sleep(10 * time.Millisecond)
	}

	// Settling turn one lets the queued turn start, and only then: its typing
	// action and its question follow the first turn's delivery.
	pushUpdates(h.bot.fakeBot, callbackUpdate(3, "cb-1", askButtonData(t, q1)[0]))
	three := h.waitRich(t, 3)
	if !strings.HasPrefix(three[1], `{"confirmed":true`) {
		t.Errorf("delivery[1] = %q, want the first turn's answer", three[1])
	}
	if !strings.Contains(three[2], "Aprobar") {
		t.Errorf("delivery[2] = %q, want the queued turn's question", three[2])
	}
	if _, _, _, typing := h.bot.snapshot(); len(typing) != 2 {
		t.Errorf("typing actions = %d, want exactly 2: one per turn, the second only after the first finished", len(typing))
	}

	time.Sleep(300 * time.Millisecond)
	if got := len(h.rich()); got != 3 {
		t.Errorf("deliveries after settling = %d (%v), want exactly 3: no turn lost or repeated", got, h.rich())
	}
	if err := h.stop(t); !errors.Is(err, context.Canceled) {
		t.Errorf("Run returned %v, want context.Canceled", err)
	}
}
