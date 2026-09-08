package sqlite

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/verdu/alter/internal/domain"
)

func openTestStore(t *testing.T) *Store {
	t.Helper()
	store, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func TestTaskCreateAndGet(t *testing.T) {
	ctx := context.Background()
	repo := openTestStore(t).NewTaskRepository()

	now := time.Now().UTC().Truncate(time.Second)
	due := now.Add(24 * time.Hour)
	want := domain.Task{
		ID:          "task-1",
		Title:       "Write report",
		Description: "Quarterly report",
		Status:      domain.TaskStatusPending,
		Priority:    domain.TaskPriorityHigh,
		DueAt:       &due,
		Source:      "cli",
		CreatedAt:   now,
		UpdatedAt:   now,
	}

	if err := repo.Create(ctx, want); err != nil {
		t.Fatalf("create: %v", err)
	}

	got, err := repo.GetByID(ctx, "task-1")
	if err != nil {
		t.Fatalf("get: %v", err)
	}

	if got.ID != want.ID || got.Title != want.Title || got.Description != want.Description {
		t.Errorf("identity/meta mismatch: %+v", got)
	}
	if got.Status != want.Status || got.Priority != want.Priority {
		t.Errorf("status/priority mismatch: got %+v", got)
	}
	if got.Source != want.Source {
		t.Errorf("source mismatch: got %q want %q", got.Source, want.Source)
	}
	if got.DueAt == nil || !got.DueAt.Equal(due) {
		t.Errorf("due_at mismatch: got %v want %v", got.DueAt, due)
	}
	if !got.CreatedAt.Equal(now) || !got.UpdatedAt.Equal(now) {
		t.Errorf("timestamps mismatch: %+v", got)
	}
}

func TestTaskUpdate(t *testing.T) {
	ctx := context.Background()
	repo := openTestStore(t).NewTaskRepository()

	now := time.Now().UTC().Truncate(time.Second)
	task := domain.Task{
		ID: "task-1", Title: "Original", Description: "",
		Status: domain.TaskStatusPending, Priority: domain.TaskPriorityLow,
		CreatedAt: now, UpdatedAt: now,
	}
	if err := repo.Create(ctx, task); err != nil {
		t.Fatalf("create: %v", err)
	}

	task.Title = "Updated"
	task.Status = domain.TaskStatusCompleted
	task.UpdatedAt = now.Add(10 * time.Second)

	if err := repo.Update(ctx, task); err != nil {
		t.Fatalf("update: %v", err)
	}

	got, err := repo.GetByID(ctx, "task-1")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Title != "Updated" || got.Status != domain.TaskStatusCompleted {
		t.Errorf("update not persisted: %+v", got)
	}
	if !got.UpdatedAt.Equal(now.Add(10 * time.Second)) {
		t.Errorf("updated_at not persisted: %v", got.UpdatedAt)
	}
}

func TestTriggerCreateAndGet(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t)
	repo := store.NewTriggerRepository()
	taskRepo := store.NewTaskRepository()

	now := time.Now().UTC().Truncate(time.Second)

	// A task must exist before a trigger due to the FK constraint.
	task := domain.Task{
		ID: "task-1", Title: "Pay rent",
		Status: domain.TaskStatusPending, Priority: domain.TaskPriorityMedium,
		CreatedAt: now, UpdatedAt: now,
	}
	if err := taskRepo.Create(ctx, task); err != nil {
		t.Fatalf("create task: %v", err)
	}

	want := domain.Trigger{
		ID:        "trg-1",
		TaskID:    "task-1",
		Type:      domain.TriggerTypeBeforeDue,
		Value:     "2h",
		Enabled:   true,
		CreatedAt: now,
	}
	if err := repo.Create(ctx, want); err != nil {
		t.Fatalf("create trigger: %v", err)
	}

	got, err := repo.GetByID(ctx, "trg-1")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.ID != want.ID || got.TaskID != want.TaskID || got.Type != want.Type {
		t.Errorf("mismatch: %+v", got)
	}
	if got.Value != "2h" || !got.Enabled {
		t.Errorf("value/enabled mismatch: %+v", got)
	}
	if !got.CreatedAt.Equal(now) {
		t.Errorf("created_at mismatch: %v", got.CreatedAt)
	}
}

func TestEventSaveAndList(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t)
	es := store.NewEventStore()

	now := time.Now().UTC().Truncate(time.Second)
	want := domain.Event{
		ID:   "evt-1",
		Type: domain.EventTaskCreated,
		Payload: map[string]any{
			"task_id": "task-1",
			"count":   3,
		},
		CreatedAt: now,
	}

	if err := es.Save(ctx, want); err != nil {
		t.Fatalf("save: %v", err)
	}

	events, err := es.ListByType(ctx, domain.EventTaskCreated)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}

	got := events[0]
	if got.ID != want.ID || got.Type != want.Type {
		t.Errorf("identity mismatch: %+v", got)
	}
	if got.Payload["task_id"] != "task-1" {
		t.Errorf("payload task_id mismatch: %v", got.Payload["task_id"])
	}
	if got.Payload["count"] != float64(3) {
		t.Errorf("payload count mismatch: expected float64(3), got %#v", got.Payload["count"])
	}
	if !got.CreatedAt.Equal(now) {
		t.Errorf("created_at mismatch: %v", got.CreatedAt)
	}
}

// seedTriggerTask inserts a valid task so trigger FK insertion can succeed.
func seedTriggerTask(t *testing.T, store *Store, id string) {
	t.Helper()
	now := time.Now().UTC().Truncate(time.Second)
	if err := store.NewTaskRepository().Create(context.Background(), domain.Task{
		ID: id, Title: "seed",
		Status: domain.TaskStatusPending, Priority: domain.TaskPriorityLow,
		CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatalf("seed task: %v", err)
	}
}

func TestTriggerFireStateCreateGet(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t)
	seedTriggerTask(t, store, "task-1")
	repo := store.NewTriggerRepository()

	now := time.Now().UTC().Truncate(time.Second)
	next := now.Add(2 * time.Hour)
	last := now.Add(-1 * time.Hour)
	tr := domain.Trigger{
		ID: "trg-1", TaskID: "task-1", Type: domain.TriggerTypeAt,
		Value: "x", Enabled: true,
		NextFireAt: &next, LastFiredAt: &last, CreatedAt: now,
	}
	if err := repo.Create(ctx, tr); err != nil {
		t.Fatalf("create: %v", err)
	}

	got, err := repo.GetByID(ctx, "trg-1")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.NextFireAt == nil || !got.NextFireAt.Equal(next) {
		t.Errorf("next_fire_at mismatch: %v want %v", got.NextFireAt, next)
	}
	if got.LastFiredAt == nil || !got.LastFiredAt.Equal(last) {
		t.Errorf("last_fired_at mismatch: %v want %v", got.LastFiredAt, last)
	}

	// GetByTaskID must recover both fields too.
	byTask, err := repo.GetByTaskID(ctx, "task-1")
	if err != nil {
		t.Fatalf("get by task: %v", err)
	}
	if len(byTask) != 1 || byTask[0].NextFireAt == nil || !byTask[0].NextFireAt.Equal(next) {
		t.Errorf("get by task fire state mismatch: %+v", byTask)
	}
}

func TestTriggerFireStateUpdate(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t)
	seedTriggerTask(t, store, "task-1")
	repo := store.NewTriggerRepository()

	now := time.Now().UTC().Truncate(time.Second)
	next1 := now.Add(time.Hour)
	tr := domain.Trigger{
		ID: "trg-1", TaskID: "task-1", Type: domain.TriggerTypeAt,
		Value: "x", Enabled: true, NextFireAt: &next1, CreatedAt: now,
	}
	if err := repo.Create(ctx, tr); err != nil {
		t.Fatalf("create: %v", err)
	}

	// Simulate a fire: recompute NextFireAt and stamp LastFiredAt.
	next2 := now.Add(24 * time.Hour)
	tr.NextFireAt = &next2
	tr.LastFiredAt = &now

	if err := repo.Update(ctx, tr); err != nil {
		t.Fatalf("update: %v", err)
	}

	got, err := repo.GetByID(ctx, "trg-1")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.NextFireAt == nil || !got.NextFireAt.Equal(next2) {
		t.Errorf("next_fire_at not updated: %v want %v", got.NextFireAt, next2)
	}
	if got.LastFiredAt == nil || !got.LastFiredAt.Equal(now) {
		t.Errorf("last_fired_at not updated: %v want %v", got.LastFiredAt, now)
	}
}

// TestTriggerFireStateReopen verifies fire-state persists across close/reopen.
func TestTriggerFireStateReopen(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "trigger-fire.db")

	now := time.Now().UTC().Truncate(time.Second)
	next := now.Add(3 * time.Hour)

	store1, err := Open(dbPath)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	seedTriggerTask(t, store1, "task-1")
	if err := store1.NewTriggerRepository().Create(ctx, domain.Trigger{
		ID: "trg-1", TaskID: "task-1", Type: domain.TriggerTypeAfterDue,
		Value: "30m", Enabled: true, NextFireAt: &next, CreatedAt: now,
	}); err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := store1.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	store2, err := Open(dbPath)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer store2.Close()

	got, err := store2.NewTriggerRepository().GetByID(ctx, "trg-1")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.NextFireAt == nil || !got.NextFireAt.Equal(next) {
		t.Errorf("fire state not persisted: %v want %v", got.NextFireAt, next)
	}
}

// TestTriggerClearDerivedNextFireAt verifies that changing a task's due date
// invalidates only its time-derived triggers' NextFireAt, preserving LastFiredAt,
// Enabled, and absolute "at" triggers.
func TestTriggerClearDerivedNextFireAt(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t)
	seedTriggerTask(t, store, "task-1")
	repo := store.NewTriggerRepository()

	now := time.Now().UTC().Truncate(time.Second)
	next := now.Add(24 * time.Hour)
	last := now.Add(-time.Hour)

	trigs := []domain.Trigger{
		{ID: "trg-before", TaskID: "task-1", Type: domain.TriggerTypeBeforeDue,
			Value: "24h", Enabled: true, NextFireAt: &next, LastFiredAt: &last, CreatedAt: now},
		{ID: "trg-after", TaskID: "task-1", Type: domain.TriggerTypeAfterDue,
			Value: "1h", Enabled: true, NextFireAt: &next, CreatedAt: now},
		{ID: "trg-at", TaskID: "task-1", Type: domain.TriggerTypeAt,
			Value: "2099-01-01T00:00:00Z", Enabled: true, NextFireAt: &next, CreatedAt: now},
	}
	for _, tr := range trigs {
		if err := repo.Create(ctx, tr); err != nil {
			t.Fatalf("create %s: %v", tr.ID, err)
		}
	}

	if err := repo.ClearDerivedNextFireAt(ctx, "task-1"); err != nil {
		t.Fatalf("clear: %v", err)
	}

	// Derived triggers: NextFireAt nil, LastFiredAt/Enabled preserved.
	before, err := repo.GetByID(ctx, "trg-before")
	if err != nil {
		t.Fatalf("get before: %v", err)
	}
	if before.NextFireAt != nil {
		t.Errorf("before_due NextFireAt should be nil, got %v", before.NextFireAt)
	}
	if before.LastFiredAt == nil || !before.LastFiredAt.Equal(last) {
		t.Errorf("before_due LastFiredAt should be preserved, got %v", before.LastFiredAt)
	}
	if !before.Enabled {
		t.Error("before_due should remain enabled")
	}

	after, err := repo.GetByID(ctx, "trg-after")
	if err != nil {
		t.Fatalf("get after: %v", err)
	}
	if after.NextFireAt != nil {
		t.Errorf("after_due NextFireAt should be nil, got %v", after.NextFireAt)
	}

	// Absolute "at" trigger untouched.
	at, err := repo.GetByID(ctx, "trg-at")
	if err != nil {
		t.Fatalf("get at: %v", err)
	}
	if at.NextFireAt == nil || !at.NextFireAt.Equal(next) {
		t.Errorf("at NextFireAt should be preserved, got %v", at.NextFireAt)
	}
}

// TestMigrationsApplyFromScratch verifies 0001 and 0002 are both applied on a
// brand-new database.
func TestMigrationsApplyFromScratch(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "fresh.db")
	store, err := Open(dbPath)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer store.Close()

	rows, err := store.db.Query(`SELECT version FROM schema_migrations ORDER BY version`)
	if err != nil {
		t.Fatalf("query versions: %v", err)
	}
	defer rows.Close()

	var versions []string
	for rows.Next() {
		var v string
		if err := rows.Scan(&v); err != nil {
			t.Fatalf("scan: %v", err)
		}
		versions = append(versions, v)
	}

	want := []string{"0001_init.sql", "0002_trigger_fire_state.sql"}
	if len(versions) != len(want) {
		t.Fatalf("expected %d migrations, got %v", len(want), versions)
	}
	for i := range want {
		if versions[i] != want[i] {
			t.Fatalf("migration order mismatch: got %v want %v", versions, want)
		}
	}
}

// TestPersistence verifies data survives a re-open of the same database file.
func TestPersistence(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "persist.db")

	now := time.Now().UTC().Truncate(time.Second)

	// First connection: write.
	store1, err := Open(dbPath)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	taskRepo := store1.NewTaskRepository()
	if err := taskRepo.Create(ctx, domain.Task{
		ID: "task-1", Title: "Persist me",
		Status: domain.TaskStatusPending, Priority: domain.TaskPriorityLow,
		CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := store1.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	// Second connection: read back.
	store2, err := Open(dbPath)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer store2.Close()

	got, err := store2.NewTaskRepository().GetByID(ctx, "task-1")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Title != "Persist me" {
		t.Errorf("data not persisted: %+v", got)
	}
}
