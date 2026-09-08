package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/verdu/alter/internal/domain"
)

// --- In-memory fakes -------------------------------------------------------

type fakeTaskRepo struct {
	tasks     map[string]domain.Task
	createErr error
}

func newFakeTaskRepo() *fakeTaskRepo {
	return &fakeTaskRepo{tasks: make(map[string]domain.Task)}
}

func (f *fakeTaskRepo) Create(_ context.Context, t domain.Task) error {
	if f.createErr != nil {
		return f.createErr
	}
	f.tasks[t.ID] = t
	return nil
}

func (f *fakeTaskRepo) GetByID(_ context.Context, id string) (domain.Task, error) {
	t, ok := f.tasks[id]
	if !ok {
		return domain.Task{}, errors.New("task not found")
	}
	return t, nil
}

func (f *fakeTaskRepo) Update(_ context.Context, t domain.Task) error {
	if _, ok := f.tasks[t.ID]; !ok {
		return errors.New("task not found")
	}
	f.tasks[t.ID] = t
	return nil
}

func (f *fakeTaskRepo) Delete(_ context.Context, id string) error {
	if _, ok := f.tasks[id]; !ok {
		return errors.New("task not found")
	}
	delete(f.tasks, id)
	return nil
}

func (f *fakeTaskRepo) List(context.Context) ([]domain.Task, error) {
	out := make([]domain.Task, 0, len(f.tasks))
	for _, t := range f.tasks {
		out = append(out, t)
	}
	return out, nil
}

func (f *fakeTaskRepo) has(id string) bool {
	_, ok := f.tasks[id]
	return ok
}

type fakeEventStore struct {
	events []domain.Event
}

func newFakeEventStore() *fakeEventStore {
	return &fakeEventStore{}
}

func (f *fakeEventStore) Save(_ context.Context, e domain.Event) error {
	f.events = append(f.events, e)
	return nil
}

func (f *fakeEventStore) ListByType(context.Context, domain.EventType) ([]domain.Event, error) {
	return nil, errors.New("not implemented in fake")
}

func (f *fakeEventStore) types() []domain.EventType {
	out := make([]domain.EventType, 0, len(f.events))
	for _, e := range f.events {
		out = append(out, e.Type)
	}
	return out
}

// --- Test harness ----------------------------------------------------------

type clock struct {
	t time.Time
}

func (c *clock) now() time.Time { return c.t }

// newHarness returns a service pre-wired with fakes and a controllable clock.
func newHarness() (*TaskService, *fakeTaskRepo, *fakeEventStore, *clock) {
	repo := newFakeTaskRepo()
	events := newFakeEventStore()
	clk := &clock{t: time.Date(2024, 1, 1, 12, 0, 0, 0, time.UTC)}

	svc := &TaskService{
		tasks:    repo,
		triggers: newFakeTriggerRepo(),
		events:   events,
		now:      clk.now,
		newID:    func() string { return "id-1" },
	}
	return svc, repo, events, clk
}

func mustCreate(t *testing.T, svc *TaskService) domain.Task {
	t.Helper()
	task, err := svc.Create(context.Background(), CreateTaskParams{
		Title:    "Do something",
		Priority: domain.TaskPriorityMedium,
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	return task
}

// --- Tests ---------------------------------------------------------------

func TestCreateStartsPending(t *testing.T) {
	svc, _, events, clk := newHarness()

	task := mustCreate(t, svc)

	if task.Status != domain.TaskStatusPending {
		t.Errorf("expected pending, got %q", task.Status)
	}
	if task.ID == "" {
		t.Error("expected a generated ID")
	}
	if !task.CreatedAt.Equal(clk.t) || !task.UpdatedAt.Equal(clk.t) {
		t.Errorf("timestamps should equal clock: %+v", task)
	}

	want := []domain.EventType{domain.EventTaskCreated}
	if got := events.types(); !eventTypesEqual(got, want) {
		t.Errorf("events mismatch: got %v want %v", got, want)
	}
	if events.events[0].Payload["task_id"] != task.ID {
		t.Errorf("event payload missing task_id: %v", events.events[0].Payload)
	}
}

func TestCreateAllocation(t *testing.T) {
	svc, _, _, _ := newHarness()

	due := time.Date(2024, 2, 1, 9, 0, 0, 0, time.UTC)
	task, err := svc.Create(context.Background(), CreateTaskParams{
		Title:    "With extras",
		Priority: domain.TaskPriorityHigh,
		DueAt:    &due,
		Source:   "cli",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if task.DueAt == nil || !task.DueAt.Equal(due) {
		t.Errorf("due_at not stored: %v", task.DueAt)
	}
	if task.Source != "cli" || task.Priority != domain.TaskPriorityHigh {
		t.Errorf("extras not stored: %+v", task)
	}
}

func TestUpdateChangesFieldAndUpdatedAt(t *testing.T) {
	svc, _, events, clk := newHarness()
	task := mustCreate(t, svc)

	clk.t = clk.t.Add(time.Hour)
	got, err := svc.Update(context.Background(), task.ID, UpdateTaskParams{
		Title:       ptr("Renamed"),
		Priority:    ptr(domain.TaskPriorityUrgent),
		Description: ptr("Updated description"),
	})
	if err != nil {
		t.Fatalf("update: %v", err)
	}

	if got.Title != "Renamed" || got.Priority != domain.TaskPriorityUrgent {
		t.Errorf("fields not updated: %+v", got)
	}
	if got.Description != "Updated description" {
		t.Errorf("description not updated: %q", got.Description)
	}
	if !got.UpdatedAt.Equal(clk.t) {
		t.Errorf("updated_at should equal clock: got %v want %v", got.UpdatedAt, clk.t)
	}
	if got.UpdatedAt.Equal(task.CreatedAt) {
		t.Error("updated_at should differ from created_at after update")
	}

	want := []domain.EventType{domain.EventTaskCreated, domain.EventTaskUpdated}
	if got := events.types(); !eventTypesEqual(got, want) {
		t.Errorf("events mismatch: got %v want %v", got, want)
	}
}

func TestUpdateNotFound(t *testing.T) {
	svc, _, _, _ := newHarness()

	_, err := svc.Update(context.Background(), "missing", UpdateTaskParams{Title: ptr("x")})
	if err == nil {
		t.Fatal("expected error for missing task")
	}
}

func TestComplete(t *testing.T) {
	svc, _, events, _ := newHarness()
	task := mustCreate(t, svc)

	got, err := svc.Complete(context.Background(), task.ID)
	if err != nil {
		t.Fatalf("complete: %v", err)
	}
	if got.Status != domain.TaskStatusCompleted {
		t.Errorf("expected completed, got %q", got.Status)
	}

	want := []domain.EventType{domain.EventTaskCreated, domain.EventTaskCompleted}
	if got := events.types(); !eventTypesEqual(got, want) {
		t.Errorf("events mismatch: got %v want %v", got, want)
	}
}

func TestCancel(t *testing.T) {
	svc, _, events, _ := newHarness()
	task := mustCreate(t, svc)

	got, err := svc.Cancel(context.Background(), task.ID)
	if err != nil {
		t.Fatalf("cancel: %v", err)
	}
	if got.Status != domain.TaskStatusCancelled {
		t.Errorf("expected cancelled, got %q", got.Status)
	}

	want := []domain.EventType{domain.EventTaskCreated, domain.EventTaskCancelled}
	if got := events.types(); !eventTypesEqual(got, want) {
		t.Errorf("events mismatch: got %v want %v", got, want)
	}
}

func TestCompleteCancelledTaskIsRejected(t *testing.T) {
	svc, _, _, _ := newHarness()
	task := mustCreate(t, svc)

	if _, err := svc.Cancel(context.Background(), task.ID); err != nil {
		t.Fatalf("cancel: %v", err)
	}
	if _, err := svc.Complete(context.Background(), task.ID); !errors.Is(err, ErrCannotComplete) {
		t.Errorf("expected ErrCannotComplete, got %v", err)
	}
}

func TestCancelCompletedTaskIsRejected(t *testing.T) {
	svc, _, _, _ := newHarness()
	task := mustCreate(t, svc)

	if _, err := svc.Complete(context.Background(), task.ID); err != nil {
		t.Fatalf("complete: %v", err)
	}
	if _, err := svc.Cancel(context.Background(), task.ID); !errors.Is(err, ErrCannotCancel) {
		t.Errorf("expected ErrCannotCancel, got %v", err)
	}
}

func TestDelete(t *testing.T) {
	svc, repo, _, _ := newHarness()
	task := mustCreate(t, svc)

	if err := svc.Delete(context.Background(), task.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if repo.has(task.ID) {
		t.Error("task should be removed from repository")
	}
}

func TestGetByIDAndList(t *testing.T) {
	svc, _, _, _ := newHarness()
	t1 := mustCreate(t, svc)

	got, err := svc.GetByID(context.Background(), t1.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.ID != t1.ID {
		t.Errorf("wrong task returned: %+v", got)
	}

	all, err := svc.List(context.Background())
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(all) != 1 {
		t.Errorf("expected 1 task, got %d", len(all))
	}
}

func TestUpdateDueAtInvalidatesDerivedTriggers(t *testing.T) {
	repo := newFakeTaskRepo()
	trigRepo := newFakeTriggerRepo()
	clk := &clock{t: time.Date(2024, 1, 1, 12, 0, 0, 0, time.UTC)}
	svc := &TaskService{
		tasks:    repo,
		triggers: trigRepo,
		events:   newFakeEventStore(),
		now:      clk.now,
		newID:    func() string { return "id-1" },
	}

	due := time.Date(2024, 1, 5, 20, 0, 0, 0, time.UTC) // Friday
	task, err := svc.Create(context.Background(), CreateTaskParams{Title: "t", DueAt: &due})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	// before_due 24h with a stale NextFireAt (Thursday 20:00) and a LastFiredAt.
	last := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	stale := time.Date(2024, 1, 4, 20, 0, 0, 0, time.UTC)
	trigRepo.triggers["trg-1"] = domain.Trigger{
		ID: "trg-1", TaskID: task.ID, Type: domain.TriggerTypeBeforeDue,
		Value: "24h", Enabled: true, NextFireAt: &stale, LastFiredAt: &last,
	}
	// An absolute "at" trigger that must NOT be cleared.
	atDue := time.Date(2024, 2, 1, 9, 0, 0, 0, time.UTC)
	trigRepo.triggers["trg-2"] = domain.Trigger{
		ID: "trg-2", TaskID: task.ID, Type: domain.TriggerTypeAt,
		Value: "2024-02-01T09:00:00Z", Enabled: true, NextFireAt: &atDue,
	}

	newDue := time.Date(2024, 1, 6, 20, 0, 0, 0, time.UTC) // Saturday
	if _, err := svc.Update(context.Background(), task.ID, UpdateTaskParams{DueAt: &newDue}); err != nil {
		t.Fatalf("update: %v", err)
	}

	if got := trigRepo.triggers["trg-1"].NextFireAt; got != nil {
		t.Errorf("derived NextFireAt should be nil, got %v", got)
	}
	if got := trigRepo.triggers["trg-1"].LastFiredAt; got == nil || !got.Equal(last) {
		t.Errorf("LastFiredAt should be preserved, got %v", got)
	}
	if !trigRepo.triggers["trg-1"].Enabled {
		t.Error("derived trigger should remain enabled")
	}
	if got := trigRepo.triggers["trg-2"].NextFireAt; got == nil || !got.Equal(atDue) {
		t.Errorf("at trigger NextFireAt must not be cleared, got %v", got)
	}
}

func TestUpdateWithoutDueAtKeepsNextFireAt(t *testing.T) {
	repo := newFakeTaskRepo()
	trigRepo := newFakeTriggerRepo()
	clk := &clock{t: time.Date(2024, 1, 1, 12, 0, 0, 0, time.UTC)}
	svc := &TaskService{
		tasks:    repo,
		triggers: trigRepo,
		events:   newFakeEventStore(),
		now:      clk.now,
		newID:    func() string { return "id-1" },
	}

	task, err := svc.Create(context.Background(), CreateTaskParams{Title: "t"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	next := time.Date(2024, 1, 4, 20, 0, 0, 0, time.UTC)
	trigRepo.triggers["trg-1"] = domain.Trigger{
		ID: "trg-1", TaskID: task.ID, Type: domain.TriggerTypeAfterDue,
		Value: "24h", Enabled: true, NextFireAt: &next,
	}

	if _, err := svc.Update(context.Background(), task.ID, UpdateTaskParams{Title: ptr("renamed")}); err != nil {
		t.Fatalf("update: %v", err)
	}
	if got := trigRepo.triggers["trg-1"].NextFireAt; got == nil || !got.Equal(next) {
		t.Errorf("NextFireAt should be preserved when DueAt unchanged, got %v", got)
	}
}

// --- Helpers --------------------------------------------------------------

func ptr[T any](v T) *T { return &v }

func eventTypesEqual(a, b []domain.EventType) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

