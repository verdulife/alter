package sqlite

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/verdu/alter/internal/domain"
)

// TaskRepository is a SQLite-backed domain.TaskRepository.
type TaskRepository struct {
	db *sql.DB
}

var _ domain.TaskRepository = (*TaskRepository)(nil)

const taskCols = `id, title, description, status, priority, due_at, source, created_at, updated_at`

// Create inserts a new task.
func (r *TaskRepository) Create(ctx context.Context, task domain.Task) error {
	_, err := r.db.ExecContext(ctx,
		`INSERT INTO tasks (`+taskCols+`) VALUES (?,?,?,?,?,?,?,?,?)`,
		task.ID,
		task.Title,
		task.Description,
		string(task.Status),
		string(task.Priority),
		nullableTime(task.DueAt),
		task.Source,
		formatTime(task.CreatedAt),
		formatTime(task.UpdatedAt),
	)
	return err
}

// GetByID retrieves a single task by ID.
func (r *TaskRepository) GetByID(ctx context.Context, id string) (domain.Task, error) {
	row := r.db.QueryRowContext(ctx,
		`SELECT `+taskCols+` FROM tasks WHERE id = ?`, id)
	return scanTask(row)
}

// Update overwrites an existing task.
func (r *TaskRepository) Update(ctx context.Context, task domain.Task) error {
	res, err := r.db.ExecContext(ctx,
		`UPDATE tasks SET
			title = ?, description = ?, status = ?, priority = ?,
			due_at = ?, source = ?, updated_at = ?
		 WHERE id = ?`,
		task.Title,
		task.Description,
		string(task.Status),
		string(task.Priority),
		nullableTime(task.DueAt),
		task.Source,
		formatTime(task.UpdatedAt),
		task.ID,
	)
	if err != nil {
		return err
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		return fmt.Errorf("task %s: %w", task.ID, sql.ErrNoRows)
	}
	return nil
}

// Delete removes a task by ID.
func (r *TaskRepository) Delete(ctx context.Context, id string) error {
	res, err := r.db.ExecContext(ctx, `DELETE FROM tasks WHERE id = ?`, id)
	if err != nil {
		return err
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		return fmt.Errorf("task %s: %w", id, sql.ErrNoRows)
	}
	return nil
}

// List returns all tasks.
func (r *TaskRepository) List(ctx context.Context) ([]domain.Task, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT `+taskCols+` FROM tasks ORDER BY created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var tasks []domain.Task
	for rows.Next() {
		t, err := scanTask(rows)
		if err != nil {
			return nil, err
		}
		tasks = append(tasks, t)
	}
	return tasks, rows.Err()
}

// rowScanner matches *sql.Row and *sql.Rows for the shared scan helper.
type rowScanner interface {
	Scan(dest ...any) error
}

// scanTask reads a single task row into a domain.Task.
func scanTask(s rowScanner) (domain.Task, error) {
	var t domain.Task
	var due sql.NullString
	var sStatus string
	var sPriority string
	var sCreated string
	var sUpdated string

	if err := s.Scan(
		&t.ID, &t.Title, &t.Description, &sStatus, &sPriority, &due,
		&t.Source, &sCreated, &sUpdated,
	); err != nil {
		return domain.Task{}, err
	}

	t.Status = domain.TaskStatus(sStatus)
	t.Priority = domain.TaskPriority(sPriority)

	var err error
	if t.CreatedAt, err = scanTime(sCreated); err != nil {
		return domain.Task{}, err
	}
	if t.UpdatedAt, err = scanTime(sUpdated); err != nil {
		return domain.Task{}, err
	}
	if t.DueAt, err = scanNullableTime(due); err != nil {
		return domain.Task{}, err
	}
	return t, nil
}
