package service

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/verdu/alter/internal/domain"
)

// fakeTriggerRepo is an in-memory domain.TriggerRepository.
type fakeTriggerRepo struct {
	triggers map[string]domain.Trigger
}

func newFakeTriggerRepo() *fakeTriggerRepo {
	return &fakeTriggerRepo{triggers: make(map[string]domain.Trigger)}
}

func (f *fakeTriggerRepo) Create(_ context.Context, tr domain.Trigger) error {
	f.triggers[tr.ID] = tr
	return nil
}

func (f *fakeTriggerRepo) GetByID(_ context.Context, id string) (domain.Trigger, error) {
	tr, ok := f.triggers[id]
	if !ok {
		return domain.Trigger{}, errors.New("trigger not found")
	}
	return tr, nil
}

func (f *fakeTriggerRepo) GetByTaskID(_ context.Context, taskID string) ([]domain.Trigger, error) {
	var out []domain.Trigger
	for _, tr := range f.triggers {
		if tr.TaskID == taskID {
			out = append(out, tr)
		}
	}
	return out, nil
}

func (f *fakeTriggerRepo) Update(_ context.Context, tr domain.Trigger) error {
	if _, ok := f.triggers[tr.ID]; !ok {
		return errors.New("trigger not found")
	}
	f.triggers[tr.ID] = tr
	return nil
}

func (f *fakeTriggerRepo) Delete(_ context.Context, id string) error {
	if _, ok := f.triggers[id]; !ok {
		return errors.New("trigger not found")
	}
	delete(f.triggers, id)
	return nil
}

func (f *fakeTriggerRepo) ClearDerivedNextFireAt(_ context.Context, taskID string) error {
	for id, tr := range f.triggers {
		if tr.TaskID == taskID && (tr.Type == domain.TriggerTypeBeforeDue || tr.Type == domain.TriggerTypeAfterDue) {
			tr.NextFireAt = nil
			tr.RetryAt = nil
			f.triggers[id] = tr
		}
	}
	return nil
}

func (f *fakeTriggerRepo) ListEnabled(context.Context) ([]domain.Trigger, error) {
	return nil, errors.New("not implemented in fake")
}

func (f *fakeTriggerRepo) has(id string) bool {
	_, ok := f.triggers[id]
	return ok
}

// setFireState lets tests stamp execution bookkeeping directly, simulating
// what a future Scheduler would write.
func (f *fakeTriggerRepo) setFireState(id string, next, last *time.Time) {
	tr := f.triggers[id]
	tr.NextFireAt = next
	tr.LastFiredAt = last
	f.triggers[id] = tr
}

// fakeRescheduler records Wake hints so tests can assert when a service asks the
// Scheduler to rescan. It is a recording domain.Rescheduler.
type fakeRescheduler struct {
	mu    sync.Mutex
	calls int
}

func (f *fakeRescheduler) Wake() {
	f.mu.Lock()
	f.calls++
	f.mu.Unlock()
}

func (f *fakeRescheduler) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

// newTriggerHarness wires a TriggerService with fakes and a controllable clock.
func newTriggerHarness() (*TriggerService, *fakeTriggerRepo, *fakeTaskRepo, *clock) {
	repo := newFakeTriggerRepo()
	tasks := newFakeTaskRepo()
	clk := &clock{t: time.Date(2024, 3, 1, 9, 30, 0, 0, time.UTC)}

	idSeq := 0
	svc := &TriggerService{
		triggers: repo,
		tasks:    tasks,
		now:      clk.now,
		newID: func() string {
			idSeq++
			return fmt.Sprintf("trg-%d", idSeq)
		},
	}
	return svc, repo, tasks, clk
}

// seedTask inserts a valid task so trigger validation passes.
func seedTask(t *testing.T, tasks *fakeTaskRepo, id string) {
	t.Helper()
	err := tasks.Create(context.Background(), domain.Task{
		ID: id, Title: "Seed",
		Status: domain.TaskStatusPending, Priority: domain.TaskPriorityLow,
		CreatedAt: time.Now(), UpdatedAt: time.Now(),
	})
	if err != nil {
		t.Fatalf("seed task: %v", err)
	}
}

func TestTriggerCreate(t *testing.T) {
	svc, repo, tasks, clk := newTriggerHarness()
	seedTask(t, tasks, "task-1")

	tr, err := svc.Create(context.Background(), CreateTriggerParams{
		TaskID:  "task-1",
		Type:    domain.TriggerTypeBeforeDue,
		Value:   "2h",
		Enabled: true,
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	if tr.ID == "" || tr.ID != "trg-1" {
		t.Errorf("unexpected ID: %q", tr.ID)
	}
	if tr.TaskID != "task-1" || tr.Type != domain.TriggerTypeBeforeDue || tr.Value != "2h" {
		t.Errorf("trigger fields mismatch: %+v", tr)
	}
	if !tr.Enabled {
		t.Error("expected Enabled true")
	}
	if !tr.CreatedAt.Equal(clk.t) {
		t.Errorf("created_at should equal clock: got %v want %v", tr.CreatedAt, clk.t)
	}
	if !repo.has(tr.ID) {
		t.Error("trigger should be persisted")
	}
}

func TestTriggerCreateDisabledByDefaultWhenRequested(t *testing.T) {
	svc, _, tasks, _ := newTriggerHarness()
	seedTask(t, tasks, "task-1")

	tr, err := svc.Create(context.Background(), CreateTriggerParams{
		TaskID: "task-1", Type: domain.TriggerTypeAt, Value: "2024-04-01T09:00:00Z",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if tr.Enabled {
		t.Error("expected Enabled false when not set explicitly")
	}
}

func TestTriggerRejectsUnknownTask(t *testing.T) {
	svc, _, _, _ := newTriggerHarness()

	_, err := svc.Create(context.Background(), CreateTriggerParams{
		TaskID: "missing-task",
		Type:   domain.TriggerTypeAt,
	})
	if !errors.Is(err, ErrTriggerTaskNotFound) {
		t.Errorf("expected ErrTriggerTaskNotFound, got %v", err)
	}
}

func TestTriggerGetByID(t *testing.T) {
	svc, _, tasks, _ := newTriggerHarness()
	seedTask(t, tasks, "task-1")

	created, err := svc.Create(context.Background(), CreateTriggerParams{
		TaskID: "task-1", Type: domain.TriggerTypeAfterDue, Value: "30m",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	got, err := svc.GetByID(context.Background(), created.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.ID != created.ID || got.Value != "30m" {
		t.Errorf("wrong trigger returned: %+v", got)
	}
}

func TestTriggerGetByTaskID(t *testing.T) {
	svc, _, tasks, _ := newTriggerHarness()
	seedTask(t, tasks, "task-1")

	for _, v := range []string{"1h", "2h"} {
		if _, err := svc.Create(context.Background(), CreateTriggerParams{
			TaskID: "task-1", Type: domain.TriggerTypeBeforeDue, Value: v,
		}); err != nil {
			t.Fatalf("create %s: %v", v, err)
		}
	}

	triggers, err := svc.GetByTaskID(context.Background(), "task-1")
	if err != nil {
		t.Fatalf("get by task: %v", err)
	}
	if len(triggers) != 2 {
		t.Errorf("expected 2 triggers, got %d", len(triggers))
	}
}

func TestTriggerUpdate(t *testing.T) {
	svc, _, tasks, _ := newTriggerHarness()
	seedTask(t, tasks, "task-1")

	created, err := svc.Create(context.Background(), CreateTriggerParams{
		TaskID: "task-1", Type: domain.TriggerTypeAt, Value: "2024-04-01T09:00:00Z",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	got, err := svc.Update(context.Background(), created.ID, UpdateTriggerParams{
		Type:  ptr(domain.TriggerTypeCustom),
		Value: ptr("my-custom-event"),
	})
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if got.Type != domain.TriggerTypeCustom || got.Value != "my-custom-event" {
		t.Errorf("update not applied: %+v", got)
	}
	if got.TaskID != "task-1" {
		t.Errorf("update must not change TaskID: %+v", got)
	}
}

func TestTriggerEnable(t *testing.T) {
	svc, _, tasks, _ := newTriggerHarness()
	seedTask(t, tasks, "task-1")

	created, err := svc.Create(context.Background(), CreateTriggerParams{
		TaskID: "task-1", Type: domain.TriggerTypeAt, Value: "x",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if created.Enabled {
		t.Fatal("precondition: trigger should start disabled")
	}

	got, err := svc.Enable(context.Background(), created.ID)
	if err != nil {
		t.Fatalf("enable: %v", err)
	}
	if !got.Enabled {
		t.Error("expected enabled after Enable")
	}
}

func TestTriggerDisable(t *testing.T) {
	svc, _, tasks, _ := newTriggerHarness()
	seedTask(t, tasks, "task-1")

	created, err := svc.Create(context.Background(), CreateTriggerParams{
		TaskID: "task-1", Type: domain.TriggerTypeAt, Value: "x", Enabled: true,
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	got, err := svc.Disable(context.Background(), created.ID)
	if err != nil {
		t.Fatalf("disable: %v", err)
	}
	if got.Enabled {
		t.Error("expected disabled after Disable")
	}
}

func TestTriggerDelete(t *testing.T) {
	svc, repo, tasks, _ := newTriggerHarness()
	seedTask(t, tasks, "task-1")

	created, err := svc.Create(context.Background(), CreateTriggerParams{
		TaskID: "task-1", Type: domain.TriggerTypeAt, Value: "x",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	if err := svc.Delete(context.Background(), created.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if repo.has(created.ID) {
		t.Error("trigger should be removed")
	}
}

func TestTriggerUpdatePreservesLastFiredAndInvalidatesNextFireAtOnValueChange(t *testing.T) {
	svc, repo, tasks, _ := newTriggerHarness()
	seedTask(t, tasks, "task-1")

	created, err := svc.Create(context.Background(), CreateTriggerParams{
		TaskID: "task-1", Type: domain.TriggerTypeAt, Value: "x", Enabled: true,
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	next := time.Now().UTC().Add(time.Hour)
	last := time.Now().UTC().Add(-time.Hour)
	repo.setFireState(created.ID, &next, &last)

	got, err := svc.Update(context.Background(), created.ID, UpdateTriggerParams{
		Value: ptr("y"),
	})
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	// NextFireAt is derived from Type/Value/DueAt: a Value change invalidates it.
	if got.NextFireAt != nil {
		t.Errorf("NextFireAt must be invalidated (nil) on Value change, got %v", got.NextFireAt)
	}
	// LastFiredAt records an actual past run and must be preserved.
	if got.LastFiredAt == nil || !got.LastFiredAt.Equal(last) {
		t.Errorf("LastFiredAt not preserved: %v want %v", got.LastFiredAt, last)
	}
	if !got.Enabled {
		t.Error("Enabled must be preserved")
	}
}

func TestTriggerUpdateNoScheduleChangeKeepsFireState(t *testing.T) {
	// When neither Type nor Value change, NextFireAt must be preserved untouched.
	svc, repo, tasks, _ := newTriggerHarness()
	seedTask(t, tasks, "task-1")

	created, err := svc.Create(context.Background(), CreateTriggerParams{
		TaskID: "task-1", Type: domain.TriggerTypeAt, Value: "x", Enabled: true,
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	next := time.Now().UTC().Add(time.Hour)
	last := time.Now().UTC().Add(-time.Hour)
	repo.setFireState(created.ID, &next, &last)

	// No fields that affect scheduling change.
	got, err := svc.Update(context.Background(), created.ID, UpdateTriggerParams{})
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if got.NextFireAt == nil || !got.NextFireAt.Equal(next) {
		t.Errorf("NextFireAt must be preserved when nothing changes, got %v", got.NextFireAt)
	}
	if got.LastFiredAt == nil || !got.LastFiredAt.Equal(last) {
		t.Errorf("LastFiredAt not preserved, got %v", got.LastFiredAt)
	}
	if !got.Enabled {
		t.Error("Enabled must be preserved")
	}
}

func TestTriggerUpdateAtValueChangeInvalidatesNextFireAt(t *testing.T) {
	// \"at 20:00 -> 22:00\" and \"at 20:00 -> 18:00\": both are Value changes on an
	// absolute \"at\" trigger and must invalidate the cached NextFireAt.
	for _, newValue := range []string{"2024-04-02T22:00:00Z", "2024-04-02T18:00:00Z"} {
		t.Run("value="+newValue, func(t *testing.T) {
			svc, repo, tasks, _ := newTriggerHarness()
			seedTask(t, tasks, "task-1")

			created, err := svc.Create(context.Background(), CreateTriggerParams{
				TaskID: "task-1", Type: domain.TriggerTypeAt, Value: "2024-04-02T20:00:00Z", Enabled: true,
			})
			if err != nil {
				t.Fatalf("create: %v", err)
			}
			stale := time.Date(2024, 4, 2, 20, 0, 0, 0, time.UTC)
			repo.setFireState(created.ID, &stale, nil)

			got, err := svc.Update(context.Background(), created.ID, UpdateTriggerParams{Value: ptr(newValue)})
			if err != nil {
				t.Fatalf("update: %v", err)
			}
			if got.NextFireAt != nil {
				t.Errorf("NextFireAt must be nil after Value change, got %v", got.NextFireAt)
			}
			if got.Value != newValue {
				t.Errorf("Value not applied: got %q want %q", got.Value, newValue)
			}
		})
	}
}

func TestTriggerUpdateBeforeDueValueChangeInvalidatesNextFireAt(t *testing.T) {
	// \"before_due 24h -> 1h\": a Value change on a derived trigger must invalidate
	// the cached NextFireAt so the Scheduler recomputes it from the new duration.
	svc, repo, tasks, _ := newTriggerHarness()
	seedTask(t, tasks, "task-1")

	created, err := svc.Create(context.Background(), CreateTriggerParams{
		TaskID: "task-1", Type: domain.TriggerTypeBeforeDue, Value: "24h", Enabled: true,
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	stale := time.Date(2024, 4, 2, 20, 0, 0, 0, time.UTC)
	repo.setFireState(created.ID, &stale, nil)

	got, err := svc.Update(context.Background(), created.ID, UpdateTriggerParams{Value: ptr("1h")})
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if got.NextFireAt != nil {
		t.Errorf("NextFireAt must be nil after Value change, got %v", got.NextFireAt)
	}
}

func TestTriggerUpdateTypeChangeInvalidatesNextFireAt(t *testing.T) {
	// \"before_due -> after_due\": a Type change invalidates the cached NextFireAt.
	svc, repo, tasks, _ := newTriggerHarness()
	seedTask(t, tasks, "task-1")

	created, err := svc.Create(context.Background(), CreateTriggerParams{
		TaskID: "task-1", Type: domain.TriggerTypeBeforeDue, Value: "24h", Enabled: true,
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	stale := time.Date(2024, 4, 2, 20, 0, 0, 0, time.UTC)
	last := time.Date(2024, 4, 1, 9, 0, 0, 0, time.UTC)
	repo.setFireState(created.ID, &stale, &last)

	got, err := svc.Update(context.Background(), created.ID, UpdateTriggerParams{Type: ptr(domain.TriggerTypeAfterDue)})
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if got.NextFireAt != nil {
		t.Errorf("NextFireAt must be nil after Type change, got %v", got.NextFireAt)
	}
	if got.Type != domain.TriggerTypeAfterDue {
		t.Errorf("Type not applied: got %q", got.Type)
	}
	if got.LastFiredAt == nil || !got.LastFiredAt.Equal(last) {
		t.Errorf("LastFiredAt must be preserved on Type change, got %v", got.LastFiredAt)
	}
}

func TestTriggerUpdateTypeAndValueChangeInvalidatesNextFireAt(t *testing.T) {
	svc, repo, tasks, _ := newTriggerHarness()
	seedTask(t, tasks, "task-1")

	created, err := svc.Create(context.Background(), CreateTriggerParams{
		TaskID: "task-1", Type: domain.TriggerTypeAt, Value: "2024-04-02T20:00:00Z", Enabled: true,
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	stale := time.Date(2024, 4, 2, 20, 0, 0, 0, time.UTC)
	repo.setFireState(created.ID, &stale, nil)

	got, err := svc.Update(context.Background(), created.ID, UpdateTriggerParams{
		Type:  ptr(domain.TriggerTypeBeforeDue),
		Value: ptr("30m"),
	})
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if got.NextFireAt != nil {
		t.Errorf("NextFireAt must be nil after Type+Value change, got %v", got.NextFireAt)
	}
	if got.Type != domain.TriggerTypeBeforeDue || got.Value != "30m" {
		t.Errorf("Type/Value not applied: %+v", got)
	}
}

// --- Wake del Scheduler (Rescheduler) --------------------------------------

func TestTriggerCreateWakesScheduler(t *testing.T) {
	svc, _, tasks, _ := newTriggerHarness()
	resched := &fakeRescheduler{}
	svc.rescheduler = resched
	seedTask(t, tasks, "task-1")

	if _, err := svc.Create(context.Background(), CreateTriggerParams{
		TaskID: "task-1", Type: domain.TriggerTypeAt, Value: "x", Enabled: true,
	}); err != nil {
		t.Fatalf("create: %v", err)
	}
	if resched.count() != 1 {
		t.Errorf("Create should Wake the Scheduler once, got %d", resched.count())
	}
}

func TestTriggerUpdateWakesSchedulerOnScheduleChange(t *testing.T) {
	svc, _, tasks, _ := newTriggerHarness()
	resched := &fakeRescheduler{}
	svc.rescheduler = resched
	seedTask(t, tasks, "task-1")

	created, err := svc.Create(context.Background(), CreateTriggerParams{
		TaskID: "task-1", Type: domain.TriggerTypeAt, Value: "x", Enabled: true,
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	// An update that changes Type/Value must Wake.
	if _, err := svc.Update(context.Background(), created.ID, UpdateTriggerParams{Value: ptr("y")}); err != nil {
		t.Fatalf("update: %v", err)
	}
	if resched.count() != 2 { // 1 from Create + 1 from Update
		t.Errorf("Update (schedule change) should Wake, total got %d", resched.count())
	}
}

func TestTriggerUpdateNoScheduleChangeDoesNotWake(t *testing.T) {
	svc, _, tasks, _ := newTriggerHarness()
	resched := &fakeRescheduler{}
	svc.rescheduler = resched
	seedTask(t, tasks, "task-1")

	created, err := svc.Create(context.Background(), CreateTriggerParams{
		TaskID: "task-1", Type: domain.TriggerTypeAt, Value: "x", Enabled: true,
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	// No Type/Value change: no Wake.
	if _, err := svc.Update(context.Background(), created.ID, UpdateTriggerParams{}); err != nil {
		t.Fatalf("update: %v", err)
	}
	if resched.count() != 1 { // only the Create woke it
		t.Errorf("Update without schedule change should not Wake, got %d", resched.count())
	}
}

func TestTriggerEnableDisableDeleteWakeScheduler(t *testing.T) {
	svc, _, tasks, _ := newTriggerHarness()
	resched := &fakeRescheduler{}
	svc.rescheduler = resched
	seedTask(t, tasks, "task-1")

	created, _ := svc.Create(context.Background(), CreateTriggerParams{
		TaskID: "task-1", Type: domain.TriggerTypeAt, Value: "x", Enabled: true,
	})

	if _, err := svc.Enable(context.Background(), created.ID); err != nil {
		t.Fatalf("enable: %v", err)
	}
	if _, err := svc.Disable(context.Background(), created.ID); err != nil {
		t.Fatalf("disable: %v", err)
	}
	if err := svc.Delete(context.Background(), created.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}

	if resched.count() != 4 { // Create + Enable + Disable + Delete
		t.Errorf("Create/Enable/Disable/Delete should each Wake, got %d", resched.count())
	}
}

func TestTriggerServicePreservesRetryAtOnEnableDisable(t *testing.T) {
	// RetryAt is operational state. Enable/Disable do NOT change scheduling, so like
	// LastFiredAt it must survive them. Only a scheduling change or a successful
	// fire clears it.
	svc, repo, tasks, _ := newTriggerHarness()
	seedTask(t, tasks, "task-1")

	created, err := svc.Create(context.Background(), CreateTriggerParams{
		TaskID: "task-1", Type: domain.TriggerTypeBeforeDue, Value: "24h", Enabled: true,
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	retry := time.Now().UTC().Add(30 * time.Second)
	tr := repo.triggers[created.ID]
	tr.RetryAt = &retry
	repo.triggers[created.ID] = tr

	enabled, err := svc.Enable(context.Background(), created.ID)
	if err != nil {
		t.Fatalf("enable: %v", err)
	}
	if enabled.RetryAt == nil || !enabled.RetryAt.Equal(retry) {
		t.Errorf("RetryAt must be preserved on Enable, got %v", enabled.RetryAt)
	}

	disabled, err := svc.Disable(context.Background(), created.ID)
	if err != nil {
		t.Fatalf("disable: %v", err)
	}
	if disabled.RetryAt == nil || !disabled.RetryAt.Equal(retry) {
		t.Errorf("RetryAt must be preserved on Disable, got %v", disabled.RetryAt)
	}
}

func TestTriggerServiceRetryAtNotAssociatedAfterScheduleChange(t *testing.T) {
	// Precision: when scheduling changes (Type/Value invalidation), a pending
	// RetryAt from the OLD scheduling must be cleared so it cannot fire against the
	// newly re-armed deadline. LastFiredAt (a historical record) is preserved.
	svc, repo, tasks, _ := newTriggerHarness()
	seedTask(t, tasks, "task-1")

	created, err := svc.Create(context.Background(), CreateTriggerParams{
		TaskID: "task-1", Type: domain.TriggerTypeAt, Value: "x", Enabled: true,
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	retry := time.Now().UTC().Add(30 * time.Second)
	stale := time.Now().UTC().Add(-time.Hour)
	repo.setFireState(created.ID, &stale, &stale)
	tr := repo.triggers[created.ID]
	tr.RetryAt = &retry
	repo.triggers[created.ID] = tr

	// Value change (scheduling change): RetryAt and NextFireAt are cleared.
	got, err := svc.Update(context.Background(), created.ID, UpdateTriggerParams{Value: ptr("y")})
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if got.RetryAt != nil {
		t.Errorf("RetryAt must be cleared on scheduling change, got %v", got.RetryAt)
	}
	if got.NextFireAt != nil {
		t.Errorf("NextFireAt must be nil on scheduling change, got %v", got.NextFireAt)
	}
	if got.LastFiredAt == nil {
		t.Error("LastFiredAt (historical) must be preserved on scheduling change")
	}
	if !got.Enabled {
		t.Error("Enabled must be preserved on scheduling change")
	}
}

func TestTaskUpdateDueAtClearsRetryAtOfDerivedTriggers(t *testing.T) {
	// Precision: a DueAt change recalcs derived NextFireAt; the stale RetryAt of a
	// derived trigger must not survive into the new scheduling.
	tasks := newFakeTaskRepo()
	trigRepo := newFakeTriggerRepo()
	clk := &clock{t: time.Date(2024, 1, 1, 12, 0, 0, 0, time.UTC)}
	svc := &TaskService{
		tasks:    tasks,
		triggers: trigRepo,
		events:   newFakeEventStore(),
		now:      clk.now,
		newID:    func() string { return "id-1" },
	}

	due := time.Date(2024, 1, 5, 20, 0, 0, 0, time.UTC)
	task, err := svc.Create(context.Background(), CreateTaskParams{Title: "t", DueAt: &due})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	stale := time.Date(2024, 1, 4, 20, 0, 0, 0, time.UTC)
	retry := time.Date(2024, 1, 4, 20, 0, 30, 0, time.UTC)
	trigRepo.triggers["trg-1"] = domain.Trigger{
		ID: "trg-1", TaskID: task.ID, Type: domain.TriggerTypeBeforeDue,
		Value: "24h", Enabled: true, NextFireAt: &stale, RetryAt: &retry,
	}

	newDue := time.Date(2024, 1, 6, 20, 0, 0, 0, time.UTC)
	if _, err := svc.Update(context.Background(), task.ID, UpdateTaskParams{DueAt: &newDue}); err != nil {
		t.Fatalf("update: %v", err)
	}

	got := trigRepo.triggers["trg-1"]
	if got.NextFireAt != nil {
		t.Errorf("derived NextFireAt must be nil after DueAt change, got %v", got.NextFireAt)
	}
	if got.RetryAt != nil {
		t.Errorf("stale RetryAt must be cleared on DueAt change, got %v", got.RetryAt)
	}
	if !got.Enabled {
		t.Error("derived trigger must remain enabled")
	}
}

func TestTriggerEnableDisablePreserveFireState(t *testing.T) {
	svc, repo, tasks, _ := newTriggerHarness()
	seedTask(t, tasks, "task-1")

	created, err := svc.Create(context.Background(), CreateTriggerParams{
		TaskID: "task-1", Type: domain.TriggerTypeAt, Value: "x", Enabled: true,
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	next := time.Now().UTC().Add(time.Hour)
	last := time.Now().UTC().Add(-time.Hour)
	repo.setFireState(created.ID, &next, &last)

	enabled, err := svc.Enable(context.Background(), created.ID)
	if err != nil {
		t.Fatalf("enable: %v", err)
	}
	if enabled.NextFireAt == nil || !enabled.NextFireAt.Equal(next) ||
		enabled.LastFiredAt == nil || !enabled.LastFiredAt.Equal(last) {
		t.Errorf("Enable altered fire state: %+v", enabled)
	}

	disabled, err := svc.Disable(context.Background(), created.ID)
	if err != nil {
		t.Fatalf("disable: %v", err)
	}
	if disabled.NextFireAt == nil || !disabled.NextFireAt.Equal(next) ||
		disabled.LastFiredAt == nil || !disabled.LastFiredAt.Equal(last) {
		t.Errorf("Disable altered fire state: %+v", disabled)
	}
}

func TestTriggerCreatedAtDeterministic(t *testing.T) {
	svc, _, tasks, clk := newTriggerHarness()
	seedTask(t, tasks, "task-1")

	before := clk.t.Add(-time.Minute)
	clk.t = clk.t.Add(time.Hour)
	want := clk.t

	tr, err := svc.Create(context.Background(), CreateTriggerParams{
		TaskID: "task-1", Type: domain.TriggerTypeAt, Value: "x",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	if !tr.CreatedAt.Equal(want) {
		t.Errorf("created_at (%v) should equal clock (%v), not %v", tr.CreatedAt, want, before)
	}
	if tr.CreatedAt.Equal(before) {
		t.Error("created_at should use the injected clock, not time.Now")
	}
}