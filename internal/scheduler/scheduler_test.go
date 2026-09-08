package scheduler

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"log"
	"sync"
	"testing"
	"time"

	"github.com/verdu/alter/internal/domain"
)

// --- Fixed clock -----------------------------------------------------------

var fixedNow = time.Date(2024, 5, 1, 10, 0, 0, 0, time.UTC)

// --- In-memory fakes -------------------------------------------------------

type fakeTriggerRepo struct {
	mu        sync.Mutex
	enabled   bool // if true, ListEnabled filters by Enabled
	triggers  map[string]domain.Trigger
	listCalls int
	listErr   error // if set, ListEnabled returns it (infra error testing)
}

func newFakeTriggerRepo() *fakeTriggerRepo {
	return &fakeTriggerRepo{enabled: true, triggers: make(map[string]domain.Trigger)}
}

func (f *fakeTriggerRepo) ListEnabled(context.Context) ([]domain.Trigger, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.listCalls++
	if f.listErr != nil {
		return nil, f.listErr
	}
	out := make([]domain.Trigger, 0, len(f.triggers))
	for _, t := range f.triggers {
		if !f.enabled || t.Enabled {
			out = append(out, t)
		}
	}
	return out, nil
}

func (f *fakeTriggerRepo) Update(_ context.Context, t domain.Trigger) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.triggers[t.ID] = t
	return nil
}

func (f *fakeTriggerRepo) Create(_ context.Context, t domain.Trigger) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.triggers[t.ID] = t
	return nil
}

func (f *fakeTriggerRepo) GetByID(_ context.Context, id string) (domain.Trigger, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	t, ok := f.triggers[id]
	if !ok {
		return domain.Trigger{}, sql.ErrNoRows
	}
	return t, nil
}

func (f *fakeTriggerRepo) GetByTaskID(_ context.Context, _ string) ([]domain.Trigger, error) {
	return nil, nil
}

func (f *fakeTriggerRepo) Delete(_ context.Context, _ string) error { return nil }

func (f *fakeTriggerRepo) ClearDerivedNextFireAt(_ context.Context, _ string) error { return nil }

func (f *fakeTriggerRepo) get(id string) (domain.Trigger, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	t, ok := f.triggers[id]
	return t, ok
}

func (f *fakeTriggerRepo) calls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.listCalls
}

type fakeTaskRepo struct {
	mu    sync.Mutex
	tasks map[string]domain.Task
	err   error
}

func newFakeTaskRepo() *fakeTaskRepo {
	return &fakeTaskRepo{tasks: make(map[string]domain.Task)}
}

func (f *fakeTaskRepo) Create(_ context.Context, t domain.Task) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.tasks[t.ID] = t
	return nil
}

func (f *fakeTaskRepo) GetByID(_ context.Context, id string) (domain.Task, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return domain.Task{}, f.err
	}
	t, ok := f.tasks[id]
	if !ok {
		return domain.Task{}, sql.ErrNoRows
	}
	return t, nil
}

func (f *fakeTaskRepo) Update(_ context.Context, t domain.Task) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.tasks[t.ID] = t
	return nil
}

func (f *fakeTaskRepo) Delete(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.tasks, id)
	return nil
}

func (f *fakeTaskRepo) List(context.Context) ([]domain.Task, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]domain.Task, 0, len(f.tasks))
	for _, t := range f.tasks {
		out = append(out, t)
	}
	return out, nil
}

func (f *fakeTaskRepo) seed(task domain.Task) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.tasks[task.ID] = task
}

type fakeEventStore struct {
	mu     sync.Mutex
	events []domain.Event
}

func newFakeEventStore() *fakeEventStore { return &fakeEventStore{} }

func (f *fakeEventStore) Save(_ context.Context, e domain.Event) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.events = append(f.events, e)
	return nil
}

func (f *fakeEventStore) ListByType(context.Context, domain.EventType) ([]domain.Event, error) {
	return nil, nil
}

func (f *fakeEventStore) fired() []domain.Event {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []domain.Event
	for _, e := range f.events {
		if e.Type == domain.EventTriggerFired {
			out = append(out, e)
		}
	}
	return out
}

type fakeAction struct {
	mu    sync.Mutex
	calls int
	fail  bool
}

func newFakeAction() *fakeAction { return &fakeAction{} }

func (f *fakeAction) Execute(context.Context, domain.Trigger, domain.Task) error {
	f.mu.Lock()
	f.calls++
	f.mu.Unlock()
	if f.fail {
		return errors.New("action failed")
	}
	return nil
}

func (f *fakeAction) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

// --- Harness ---------------------------------------------------------------

func newHarness(t *testing.T, action domain.TriggerAction, opts ...Option) (*Scheduler, *fakeTriggerRepo, *fakeTaskRepo, *fakeEventStore) {
	t.Helper()
	trig := newFakeTriggerRepo()
	tasks := newFakeTaskRepo()
	events := newFakeEventStore()
	discard := log.New(io.Discard, "", 0)
	s := NewScheduler(trig, tasks, events, action,
		append([]Option{
			WithNow(func() time.Time { return fixedNow }),
			WithLogger(discard),
			WithID(func() string { return "evt-1" }),
		}, opts...)...)
	return s, trig, tasks, events
}

func mustTick(t *testing.T, s *Scheduler) time.Time {
	t.Helper()
	next, err := s.tick(context.Background())
	if err != nil {
		t.Fatalf("tick: %v", err)
	}
	return next
}

func waitFor(t *testing.T, timeout time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("condition not met within %v", timeout)
}

func dueTask(id string) domain.Task {
	return domain.Task{ID: id, Title: "t", Status: domain.TaskStatusPending, Priority: domain.TaskPriorityLow}
}

// --- Construction ----------------------------------------------------------

func TestNilActionPanics(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Error("expected panic for nil TriggerAction")
		}
	}()
	NewScheduler(newFakeTriggerRepo(), newFakeTaskRepo(), newFakeEventStore(), nil)
}

// --- ARMAR -----------------------------------------------------------------

func TestArmAt(t *testing.T) {
	s, trig, tasks, _ := newHarness(t, newFakeAction())
	tasks.seed(dueTask("t"))
	_ = trig.Create(context.Background(), domain.Trigger{ID: "a", TaskID: "t", Type: domain.TriggerTypeAt, Value: "2024-05-02T09:00:00Z", Enabled: true})

	next := mustTick(t, s)
	want := time.Date(2024, 5, 2, 9, 0, 0, 0, time.UTC)
	if !next.Equal(want) {
		t.Errorf("next deadline = %v, want %v", next, want)
	}
	got, _ := trig.get("a")
	if got.NextFireAt == nil || !got.NextFireAt.Equal(want) {
		t.Errorf("trigger not armed: NextFireAt=%v", got.NextFireAt)
	}
	if !got.Enabled {
		t.Error("armed trigger should remain enabled")
	}
}

func TestArmBeforeDue(t *testing.T) {
	s, trig, tasks, _ := newHarness(t, newFakeAction())
	due := time.Date(2024, 5, 2, 20, 0, 0, 0, time.UTC)
	task := dueTask("t")
	task.DueAt = &due
	tasks.seed(task)
	_ = trig.Create(context.Background(), domain.Trigger{ID: "a", TaskID: "t", Type: domain.TriggerTypeBeforeDue, Value: "24h", Enabled: true})

	next := mustTick(t, s)
	want := time.Date(2024, 5, 1, 20, 0, 0, 0, time.UTC)
	if !next.Equal(want) {
		t.Errorf("next deadline = %v, want %v", next, want)
	}
}

func TestArmAfterDue(t *testing.T) {
	s, trig, tasks, _ := newHarness(t, newFakeAction())
	due := time.Date(2024, 5, 2, 20, 0, 0, 0, time.UTC)
	task := dueTask("t")
	task.DueAt = &due
	tasks.seed(task)
	_ = trig.Create(context.Background(), domain.Trigger{ID: "a", TaskID: "t", Type: domain.TriggerTypeAfterDue, Value: "1h", Enabled: true})

	next := mustTick(t, s)
	want := time.Date(2024, 5, 2, 21, 0, 0, 0, time.UTC)
	if !next.Equal(want) {
		t.Errorf("next deadline = %v, want %v", next, want)
	}
}

func TestArmThenFireWhenAlreadyDue(t *testing.T) {
	// An unarmed "at" trigger whose timestamp is already in the past is armed and
	// then fired in the same cycle (overdue recovery).
	s, trig, tasks, events := newHarness(t, newFakeAction())
	tasks.seed(dueTask("t"))
	_ = trig.Create(context.Background(), domain.Trigger{ID: "a", TaskID: "t", Type: domain.TriggerTypeAt, Value: "2024-05-01T08:00:00Z", Enabled: true})

	next := mustTick(t, s)
	if !next.IsZero() {
		t.Errorf("expected no future deadline, got %v", next)
	}
	got, _ := trig.get("a")
	if got.Enabled {
		t.Error("trigger should have fired (disabled)")
	}
	if got.LastFiredAt == nil || !got.LastFiredAt.Equal(fixedNow) {
		t.Errorf("LastFiredAt should be %v, got %v", fixedNow, got.LastFiredAt)
	}
	if len(events.fired()) != 1 {
		t.Errorf("expected 1 fired event, got %d", len(events.fired()))
	}
}

// --- DISPARAR --------------------------------------------------------------

func TestFireDueTrigger(t *testing.T) {
	action := newFakeAction()
	s, trig, tasks, events := newHarness(t, action)
	_ = trig.Create(context.Background(), domain.Trigger{ID: "a", TaskID: "t", Type: domain.TriggerTypeAt, Enabled: true, NextFireAt: &fixedNow})
	tasks.seed(dueTask("t"))

	mustTick(t, s)

	got, _ := trig.get("a")
	if got.Enabled {
		t.Error("trigger should be disabled after firing")
	}
	if got.NextFireAt != nil {
		t.Errorf("NextFireAt should be nil after one-shot, got %v", got.NextFireAt)
	}
	if got.LastFiredAt == nil || !got.LastFiredAt.Equal(fixedNow) {
		t.Errorf("LastFiredAt should be %v, got %v", fixedNow, got.LastFiredAt)
	}
	if action.count() != 1 {
		t.Errorf("action should be called once, got %d", action.count())
	}
	fired := events.fired()
	if len(fired) != 1 || fired[0].Payload["trigger_id"] != "a" || fired[0].Payload["task_id"] != "t" {
		t.Errorf("unexpected fired event: %+v", fired)
	}
}

func TestFireOverdueTrigger(t *testing.T) {
	s, trig, tasks, _ := newHarness(t, newFakeAction())
	overdue := fixedNow.Add(-2 * time.Hour)
	_ = trig.Create(context.Background(), domain.Trigger{ID: "a", TaskID: "t", Type: domain.TriggerTypeAt, Enabled: true, NextFireAt: &overdue})
	tasks.seed(dueTask("t"))

	mustTick(t, s)

	got, _ := trig.get("a")
	if got.Enabled {
		t.Error("overdue trigger should still fire (disabled)")
	}
	if got.LastFiredAt == nil || !got.LastFiredAt.Equal(fixedNow) {
		t.Errorf("LastFiredAt should be %v, got %v", fixedNow, got.LastFiredAt)
	}
}

func TestMultipleDueTriggersAllFire(t *testing.T) {
	s, trig, tasks, events := newHarness(t, newFakeAction())
	next1 := fixedNow.Add(-time.Hour)
	next2 := fixedNow // same/overlapping deadlines
	for _, id := range []string{"a", "b"} {
		_ = trig.Create(context.Background(), domain.Trigger{ID: id, TaskID: "t", Type: domain.TriggerTypeAt, Enabled: true, NextFireAt: &next1})
	}
	_ = trig.Create(context.Background(), domain.Trigger{ID: "c", TaskID: "t", Type: domain.TriggerTypeAt, Enabled: true, NextFireAt: &next2})
	tasks.seed(dueTask("t"))

	mustTick(t, s)

	for _, id := range []string{"a", "b", "c"} {
		got, _ := trig.get(id)
		if got.Enabled {
			t.Errorf("trigger %s should have fired", id)
		}
	}
	if n := len(events.fired()); n != 3 {
		t.Errorf("expected 3 fired events, got %d", n)
	}
}

func TestAllDueTriggersProcessedBeforePlanning(t *testing.T) {
	// Processing must FINISH all due triggers before returning; a later due
	// trigger is not skipped because an earlier one fired first.
	action := newFakeAction()
	s, trig, tasks, events := newHarness(t, action)
	past := fixedNow.Add(-time.Hour)
	_ = trig.Create(context.Background(), domain.Trigger{ID: "a", TaskID: "t", Type: domain.TriggerTypeAt, Enabled: true, NextFireAt: &past})
	_ = trig.Create(context.Background(), domain.Trigger{ID: "b", TaskID: "t", Type: domain.TriggerTypeAt, Enabled: true, NextFireAt: &past})
	tasks.seed(dueTask("t"))

	next := mustTick(t, s)
	if !next.IsZero() {
		t.Errorf("no future deadline expected, got %v", next)
	}
	if action.count() != 2 {
		t.Errorf("expected both due triggers actioned, got %d", action.count())
	}
	if len(events.fired()) != 2 {
		t.Errorf("expected 2 events, got %d", len(events.fired()))
	}
}

func TestBackoffSurvivesWake(t *testing.T) {
	// After an action failure sets a RetryAt backoff, a Wake rescan must re-read
	// SQLite and must NOT re-attempt the action while the backoff is still pending
	// (the persisted RetryAt is the source of truth).
	action := newFakeAction()
	action.fail = true
	s, trig, tasks, _ := newHarness(t, action)
	_ = trig.Create(context.Background(), domain.Trigger{ID: "a", TaskID: "t", Type: domain.TriggerTypeAt, Enabled: true, NextFireAt: &fixedNow})
	tasks.seed(dueTask("t"))

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Run(ctx) }()

	waitFor(t, 2*time.Second, func() bool { return action.count() >= 1 })

	before := trig.calls()
	s.Wake()
	waitFor(t, 2*time.Second, func() bool { return trig.calls() > before })

	// The action must not re-run: the RetryAt backoff keeps the trigger non-due and
	// Wake merely triggered a rescan that re-read the persisted RetryAt.
	if action.count() != 1 {
		t.Errorf("Wake must not force an immediate retry during backoff, got %d attempts", action.count())
	}

	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not stop")
	}
}

// --- Acción ----------------------------------------------------------------

func TestActionFailureDoesNotConsumeTrigger(t *testing.T) {
	action := newFakeAction()
	action.fail = true
	action.calls = 0
	s, trig, tasks, events := newHarness(t, action)
	_ = trig.Create(context.Background(), domain.Trigger{ID: "a", TaskID: "t", Type: domain.TriggerTypeAt, Enabled: true, NextFireAt: &fixedNow})
	tasks.seed(dueTask("t"))

	mustTick(t, s)

	if action.count() != 1 {
		t.Errorf("action should be attempted once, got %d", action.count())
	}
	got, _ := trig.get("a")
	if !got.Enabled {
		t.Error("trigger must remain enabled when action fails (not consumed)")
	}
	// At-least-once: the trigger is NOT consumed and retries later, but a RetryAt
	// backoff holds it off so it is no longer immediately due (no busy loop). The
	// derived deadline NextFireAt is left untouched.
	wantRetry := fixedNow.Add(defaultActionRetryDelay)
	if got.NextFireAt == nil || !got.NextFireAt.Equal(fixedNow) {
		t.Errorf("NextFireAt (derived deadline) must be preserved untouched, got %v", got.NextFireAt)
	}
	if got.RetryAt == nil {
		t.Fatal("RetryAt must be set after action failure (backoff)")
	}
	if !got.RetryAt.Equal(wantRetry) {
		t.Errorf("RetryAt should be %v, got %v", wantRetry, got.RetryAt)
	}
	if got.LastFiredAt != nil {
		t.Errorf("LastFiredAt must not be set on action failure, got %v", got.LastFiredAt)
	}
	if n := len(events.fired()); n != 0 {
		t.Errorf("no fired event expected on action failure, got %d", n)
	}
}

func TestActionFailureBackoff(t *testing.T) {
	// The RetryAt backoff must re-arm the scheduler's next deadline to RetryAt
	// (now + defaultActionRetryDelay), so the loop wakes for the retry instead of
	// busy-looping on the original due time or sleeping indefinitely.
	action := newFakeAction()
	action.fail = true
	s, trig, tasks, _ := newHarness(t, action)
	_ = trig.Create(context.Background(), domain.Trigger{ID: "a", TaskID: "t", Type: domain.TriggerTypeAt, Enabled: true, NextFireAt: &fixedNow})
	tasks.seed(dueTask("t"))

	next := mustTick(t, s)
	if next.IsZero() {
		t.Fatal("expected a future deadline from the RetryAt backoff, got zero")
	}
	want := fixedNow.Add(defaultActionRetryDelay)
	if !next.Equal(want) {
		t.Errorf("next deadline should be %v (RetryAt), got %v", want, next)
	}

	// Running the same tick again while the backoff is still pending must NOT
	// re-attempt the action (the trigger is held off by RetryAt).
	mustTick(t, s)
	if action.count() != 1 {
		t.Errorf("action must not re-run while RetryAt pending, got %d attempts", action.count())
	}
}

func TestActionFailureRecoversAfterBackoff(t *testing.T) {
	// Advancing the clock past the RetryAt backoff makes the trigger due again; a
	// retry then runs. NextFireAt (the derived deadline) is not consumed by the
	// earlier failure.
	action := newFakeAction()
	action.fail = true
	s, trig, tasks, _ := newHarness(t, action, WithNow(func() time.Time { return fixedNow.Add(defaultActionRetryDelay) }))
	_ = trig.Create(context.Background(), domain.Trigger{ID: "a", TaskID: "t", Type: domain.TriggerTypeAt, Enabled: true, NextFireAt: &fixedNow})
	tasks.seed(dueTask("t"))

	mustTick(t, s)
	if action.count() != 1 {
		t.Errorf("action should run once when RetryAt elapsed, got %d", action.count())
	}
}

func TestSuccessfulFireClearsRetryAt(t *testing.T) {
	// A trigger with an elapsed RetryAt and a succeeding action must fire normally:
	// RetryAt is cleared, NextFireAt nil, Enabled false (one-shot done).
	action := newFakeAction()
	s, trig, tasks, events := newHarness(t, action)
	retry := fixedNow.Add(-time.Minute) // already elapsed
	_ = trig.Create(context.Background(), domain.Trigger{ID: "a", TaskID: "t", Type: domain.TriggerTypeAt, Enabled: true, NextFireAt: &fixedNow, RetryAt: &retry})
	tasks.seed(dueTask("t"))

	mustTick(t, s)
	if action.count() != 1 {
		t.Errorf("action should run once, got %d", action.count())
	}
	got, _ := trig.get("a")
	if got.Enabled {
		t.Error("trigger should be disabled after successful fire")
	}
	if got.NextFireAt != nil {
		t.Errorf("NextFireAt should be nil after fire, got %v", got.NextFireAt)
	}
	if got.RetryAt != nil {
		t.Errorf("RetryAt must be cleared after successful fire, got %v", got.RetryAt)
	}
	if got.LastFiredAt == nil || !got.LastFiredAt.Equal(fixedNow) {
		t.Errorf("LastFiredAt should record the fire, got %v", got.LastFiredAt)
	}
	if n := len(events.fired()); n != 1 {
		t.Errorf("expected 1 fired event, got %d", n)
	}
}

func TestRetryAtHoldsOffDueEvenWhenNextFirePast(t *testing.T) {
	// Regression: a trigger whose derived NextFireAt is in the past must NOT fire
	// while a future RetryAt is still pending (separation of derived vs retry state).
	action := newFakeAction()
	s, trig, tasks, _ := newHarness(t, action)
	retry := fixedNow.Add(defaultActionRetryDelay) // still pending
	_ = trig.Create(context.Background(), domain.Trigger{ID: "a", TaskID: "t", Type: domain.TriggerTypeAt, Enabled: true, NextFireAt: &fixedNow, RetryAt: &retry})
	tasks.seed(dueTask("t"))

	next := mustTick(t, s)
	if action.count() != 0 {
		t.Errorf("action must not run while RetryAt pending, got %d", action.count())
	}
	// The loop still wakes at the pending RetryAt.
	if next.IsZero() || !next.Equal(retry) {
		t.Errorf("next deadline should be the pending RetryAt %v, got %v", retry, next)
	}
}

func TestShutdownDuringRetryWait(t *testing.T) {
	// An action failure sets a RetryAt backoff; cancelling while the scheduler
	// waits on that deadline must still stop Run cleanly.
	action := newFakeAction()
	action.fail = true
	s, trig, tasks, _ := newHarness(t, action)
	_ = trig.Create(context.Background(), domain.Trigger{ID: "a", TaskID: "t", Type: domain.TriggerTypeAt, Enabled: true, NextFireAt: &fixedNow})
	tasks.seed(dueTask("t"))

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Run(ctx) }()

	waitFor(t, 2*time.Second, func() bool { return action.count() >= 1 })

	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("expected context.Canceled, got %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return on ctx cancellation during retry wait")
	}
}

func TestRepositoryListEnabledErrorAbortsCycle(t *testing.T) {
	// A persistent infrastructure error from ListEnabled must abort the cycle and
	// put Run into a wait (wake/ctx) instead of churning the repository in a loop.
	s, trig, _, _ := newHarness(t, newFakeAction())
	trig.listErr = errors.New("sqlite: database is locked")

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Run(ctx) }()

	// Let a few loops run, then confirm the abort path keeps it from spinning the
	// repository: only the initial failed reads happen, then it waits.
	time.Sleep(100 * time.Millisecond)
	calls := trig.calls()
	time.Sleep(100 * time.Millisecond)
	if n := trig.calls(); n > calls+1 {
		t.Errorf("Run must not busy-loop on ListEnabled error, calls grew %d -> %d", calls, n)
	}

	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not stop")
	}
}

// --- Errores terminales vs transitorios -------------------------------------

func TestCompletedTaskRetiresTrigger(t *testing.T) {
	s, trig, tasks, events := newHarness(t, newFakeAction())
	_ = trig.Create(context.Background(), domain.Trigger{ID: "a", TaskID: "t", Type: domain.TriggerTypeAt, Enabled: true, NextFireAt: &fixedNow})
	task := dueTask("t")
	task.Status = domain.TaskStatusCompleted
	tasks.seed(task)

	mustTick(t, s)

	got, _ := trig.get("a")
	if got.Enabled {
		t.Error("terminal trigger should be retired (disabled)")
	}
	if n := len(events.fired()); n != 0 {
		t.Errorf("no fired event for terminal trigger, got %d", n)
	}
}

func TestCancelledTaskRetiresTrigger(t *testing.T) {
	s, trig, tasks, _ := newHarness(t, newFakeAction())
	_ = trig.Create(context.Background(), domain.Trigger{ID: "a", TaskID: "t", Type: domain.TriggerTypeAt, Enabled: true, NextFireAt: &fixedNow})
	task := dueTask("t")
	task.Status = domain.TaskStatusCancelled
	tasks.seed(task)

	mustTick(t, s)

	got, _ := trig.get("a")
	if got.Enabled {
		t.Error("terminal trigger should be retired (disabled)")
	}
}

func TestMissingTaskRetiresTrigger(t *testing.T) {
	s, trig, _, _ := newHarness(t, newFakeAction())
	_ = trig.Create(context.Background(), domain.Trigger{ID: "a", TaskID: "missing", Type: domain.TriggerTypeAt, Enabled: true, NextFireAt: &fixedNow})

	mustTick(t, s)

	got, _ := trig.get("a")
	if got.Enabled {
		t.Error("orphan trigger should be retired (disabled)")
	}
}

func TestTransientTaskErrorNoRetire(t *testing.T) {
	s, trig, tasks, _ := newHarness(t, newFakeAction())
	_ = trig.Create(context.Background(), domain.Trigger{ID: "a", TaskID: "t", Type: domain.TriggerTypeAt, Enabled: true, NextFireAt: &fixedNow})
	tasks.err = errors.New("db down")

	mustTick(t, s)

	got, _ := trig.get("a")
	if !got.Enabled {
		t.Error("transient task load error must NOT retire the trigger")
	}
	if got.LastFiredAt != nil {
		t.Error("no fire on transient error")
	}
}

func TestMalformedTriggerNotArmedNoFire(t *testing.T) {
	s, trig, tasks, events := newHarness(t, newFakeAction())
	tasks.seed(dueTask("t"))
	_ = trig.Create(context.Background(), domain.Trigger{ID: "a", TaskID: "t", Type: domain.TriggerTypeAt, Value: "not-a-time", Enabled: true})

	next := mustTick(t, s)
	if !next.IsZero() {
		t.Errorf("malformed trigger must contribute no deadline, got %v", next)
	}
	got, _ := trig.get("a")
	if got.NextFireAt != nil {
		t.Errorf("malformed trigger must stay unarmed, got %v", got.NextFireAt)
	}
	if !got.Enabled {
		t.Error("malformed (reversible) trigger must remain enabled")
	}
	if got.LastFiredAt != nil {
		t.Error("malformed trigger must not fire")
	}
	if n := len(events.fired()); n != 0 {
		t.Errorf("no fired event expected, got %d", n)
	}
}

// --- PLANIFICAR -------------------------------------------------------------

func TestNearestFutureDeadline(t *testing.T) {
	s, trig, tasks, _ := newHarness(t, newFakeAction())
	far := fixedNow.Add(5 * time.Hour)
	near := fixedNow.Add(1 * time.Hour)
	_ = trig.Create(context.Background(), domain.Trigger{ID: "a", TaskID: "t", Type: domain.TriggerTypeAt, Enabled: true, NextFireAt: &far})
	_ = trig.Create(context.Background(), domain.Trigger{ID: "b", TaskID: "t", Type: domain.TriggerTypeAt, Enabled: true, NextFireAt: &near})
	tasks.seed(dueTask("t"))

	next := mustTick(t, s)
	if !next.Equal(near) {
		t.Errorf("nearest deadline = %v, want %v", next, near)
	}
}

func TestNoSchedulableTriggersZeroDeadline(t *testing.T) {
	s, _, _, _ := newHarness(t, newFakeAction())
	next := mustTick(t, s)
	if !next.IsZero() {
		t.Errorf("expected zero deadline, got %v", next)
	}
}

// --- Wake ------------------------------------------------------------------

func TestWakeCoalescing(t *testing.T) {
	s, _, _, _ := newHarness(t, newFakeAction())
	for i := 0; i < 20; i++ {
		s.Wake()
	}
	delivered := 0
	for {
		select {
		case <-s.wake:
			delivered++
		default:
			return
		}
		if delivered > 1 {
			t.Fatal("Wake must coalesce to at most 1 pending")
		}
	}
}

func TestWakeIsNonBlocking(t *testing.T) {
	s, _, _, _ := newHarness(t, newFakeAction())
	// Wake from many goroutines must never block.
	done := make(chan struct{})
	for i := 0; i < 32; i++ {
		go func() { s.Wake(); done <- struct{}{} }()
	}
	for i := 0; i < 32; i++ {
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Fatal("Wake blocked a caller")
		}
	}
}

func TestWakeTriggersRescanAndProcess(t *testing.T) {
	action := newFakeAction()
	s, trig, tasks, events := newHarness(t, action)

	done := make(chan error, 1)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { done <- s.Run(ctx) }()

	// Wait for the startup cycle.
	waitFor(t, 2*time.Second, func() bool { return trig.calls() >= 1 })

	// A due trigger appears, then Wake hints a rescan.
	tasks.seed(dueTask("t"))
	_ = trig.Create(context.Background(), domain.Trigger{ID: "a", TaskID: "t", Type: domain.TriggerTypeAt, Enabled: true, NextFireAt: &fixedNow})
	s.Wake()
	s.Wake() // coalesced

	waitFor(t, 2*time.Second, func() bool { return trig.calls() >= 2 })

	got, _ := trig.get("a")
	if got.Enabled {
		t.Error("wake-led rescan should have processed the due trigger")
	}
	if len(events.fired()) != 1 {
		t.Errorf("expected 1 fired event, got %d", len(events.fired()))
	}
}

// --- Shutdown y ausencia de polling -----------------------------------------

func TestCleanShutdown(t *testing.T) {
	s, _, _, _ := newHarness(t, newFakeAction())
	done := make(chan error, 1)
	ctx, cancel := context.WithCancel(context.Background())
	go func() { done <- s.Run(ctx) }()

	time.Sleep(20 * time.Millisecond) // let Run enter the wait
	cancel()

	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("expected context.Canceled, got %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return on ctx cancellation")
	}
}

func TestNoPollingWithoutDeadline(t *testing.T) {
	s, trig, _, _ := newHarness(t, newFakeAction())
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Run(ctx) }()

	// Wait for the single startup cycle.
	waitFor(t, 2*time.Second, func() bool { return trig.calls() >= 1 })

	// With no due/future triggers there is no deadline; the loop must wait on
	// wake/ctx only and NOT poll the repository.
	time.Sleep(150 * time.Millisecond)
	if n := trig.calls(); n != 1 {
		t.Errorf("no polling expected (1 startup read), got %d reads", n)
	}

	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not stop")
	}
}

func TestDueDuringRuntimeViaTimer(t *testing.T) {
	// Exercises the real single-timer path (no polling): a future deadline must
	// wake the loop once real time passes. Uses the scheduler's real clock.
	action := newFakeAction()
	trig := newFakeTriggerRepo()
	tasks := newFakeTaskRepo()
	events := newFakeEventStore()
	s := NewScheduler(trig, tasks, events, action,
		WithLogger(log.New(io.Discard, "", 0)), WithID(func() string { return "evt-1" }))

	task := dueTask("t")
	tasks.seed(task)
	next := time.Now().Add(60 * time.Millisecond)
	_ = trig.Create(context.Background(), domain.Trigger{ID: "a", TaskID: "t", Type: domain.TriggerTypeAt, Enabled: true, NextFireAt: &next})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Run(ctx) }()
	defer func() {
		cancel()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
		}
	}()

	waitFor(t, 3*time.Second, func() bool {
		got, _ := trig.get("a")
		return !got.Enabled
	})
	if action.count() != 1 {
		t.Errorf("expected trigger to fire once via timer, got %d actions", action.count())
	}
}
func TestRetryBackoffUsesTimerNotBusyLoop(t *testing.T) {
	// Runtime proof against a busy loop: an always-failing action with a short
	// RetryAt backoff must produce a bounded number of attempts proportional to the
	// backoff (the loop sleeps on a real timer), not an immediate spin.
	action := newFakeAction()
	action.fail = true
	trig := newFakeTriggerRepo()
	tasks := newFakeTaskRepo()
	events := newFakeEventStore()
	s := NewScheduler(trig, tasks, events, action,
		WithLogger(log.New(io.Discard, "", 0)),
		WithID(func() string { return "evt-1" }),
		WithActionRetryDelay(50*time.Millisecond),
	)

	next := time.Now()
	_ = trig.Create(context.Background(), domain.Trigger{ID: "a", TaskID: "t", Type: domain.TriggerTypeAt, Enabled: true, NextFireAt: &next})
	tasks.seed(dueTask("t"))

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Run(ctx) }()
	defer func() {
		cancel()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
		}
	}()

	waitFor(t, 3*time.Second, func() bool { return action.count() >= 2 })
	time.Sleep(300 * time.Millisecond)

	calls := action.count()
	// With a 50ms backoff over ~400ms: expect a handful of attempts (~8), never a
	// tight spin. A busy loop (0 delay) would produce hundreds.
	if calls < 2 {
		t.Errorf("expected retries, got %d", calls)
	}
	if calls > 25 {
		t.Errorf("busy loop detected: %d attempts over ~400ms (wanted bounded by RetryAt backoff)", calls)
	}
	got, _ := trig.get("a")
	if !got.Enabled {
		t.Error("always-failing action must not consume the trigger")
	}
	if got.LastFiredAt != nil {
		t.Error("no fire should be recorded for a failing action")
	}
}

func TestRetryWakesSchedulerAndFiresWhenElapsed(t *testing.T) {
	// Runtime proof that a future RetryAt wakes the loop at the right moment and
	// the retry then succeeds: the action fails once, then the scheduler sleeps on
	// a timer until RetryAt elapses and re-attempts, which fires and consumes it.
	action := &failOnceSuccessAction{}
	trig := newFakeTriggerRepo()
	tasks := newFakeTaskRepo()
	events := newFakeEventStore()
	s := NewScheduler(trig, tasks, events, action,
		WithLogger(log.New(io.Discard, "", 0)),
		WithID(func() string { return "evt-1" }),
		WithActionRetryDelay(50*time.Millisecond),
	)

	next := time.Now()
	_ = trig.Create(context.Background(), domain.Trigger{ID: "a", TaskID: "t", Type: domain.TriggerTypeAt, Enabled: true, NextFireAt: &next})
	tasks.seed(dueTask("t"))

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Run(ctx) }()
	defer func() {
		cancel()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
		}
	}()

	// The fire's completion signal is the emitted EventTriggerFired, which the
	// scheduler records AFTER persisting the disabled trigger (persist -> emit).
	// Waiting on !Enabled would race that emit and read 0 events transiently, so
	// poll the actual event emission instead.
	waitFor(t, 3*time.Second, func() bool { return len(events.fired()) >= 1 })

	if action.count() != 2 {
		t.Errorf("expected exactly 2 attempts (1 failure, 1 retry), got %d", action.count())
	}
	got, _ := trig.get("a")
	if got.RetryAt != nil {
		t.Errorf("RetryAt must be nil after a successful fire, got %v", got.RetryAt)
	}
	if n := len(events.fired()); n != 1 {
		t.Errorf("expected exactly 1 fired event, got %d", n)
	}
}

// failOnceSuccessAction fails its first attempt then succeeds: it models a
// transient action error that a single retry resolves.
type failOnceSuccessAction struct {
	mu    sync.Mutex
	calls int
}

func (a *failOnceSuccessAction) Execute(context.Context, domain.Trigger, domain.Task) error {
	a.mu.Lock()
	a.calls++
	first := a.calls == 1
	a.mu.Unlock()
	if first {
		return errors.New("transient action failure")
	}
	return nil
}

func (a *failOnceSuccessAction) count() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.calls
}
