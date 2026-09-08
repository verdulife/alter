package service

import (
	"context"
	"errors"
	"fmt"
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

func TestTriggerUpdatePreservesFireState(t *testing.T) {
	svc, repo, tasks, _ := newTriggerHarness()
	seedTask(t, tasks, "task-1")

	created, err := svc.Create(context.Background(), CreateTriggerParams{
		TaskID: "task-1", Type: domain.TriggerTypeAt, Value: "x",
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
	if got.NextFireAt == nil || !got.NextFireAt.Equal(next) {
		t.Errorf("NextFireAt not preserved: %v want %v", got.NextFireAt, next)
	}
	if got.LastFiredAt == nil || !got.LastFiredAt.Equal(last) {
		t.Errorf("LastFiredAt not preserved: %v want %v", got.LastFiredAt, last)
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