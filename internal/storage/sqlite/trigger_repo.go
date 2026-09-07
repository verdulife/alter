package sqlite

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/verdu/alter/internal/domain"
)

// TriggerRepository is a SQLite-backed domain.TriggerRepository.
type TriggerRepository struct {
	db *sql.DB
}

var _ domain.TriggerRepository = (*TriggerRepository)(nil)

const triggerCols = `id, task_id, type, value, enabled, next_fire_at, last_fired_at, created_at`

// Create inserts a new trigger.
func (r *TriggerRepository) Create(ctx context.Context, trigger domain.Trigger) error {
	_, err := r.db.ExecContext(ctx,
		`INSERT INTO triggers (`+triggerCols+`) VALUES (?,?,?,?,?,?,?,?)`,
		trigger.ID,
		trigger.TaskID,
		string(trigger.Type),
		trigger.Value,
		boolToInt(trigger.Enabled),
		nullableTime(trigger.NextFireAt),
		nullableTime(trigger.LastFiredAt),
		formatTime(trigger.CreatedAt),
	)
	return err
}

// GetByID retrieves a single trigger by ID.
func (r *TriggerRepository) GetByID(ctx context.Context, id string) (domain.Trigger, error) {
	row := r.db.QueryRowContext(ctx,
		`SELECT `+triggerCols+` FROM triggers WHERE id = ?`, id)
	return scanTrigger(row)
}

// GetByTaskID returns all triggers associated with a task.
func (r *TriggerRepository) GetByTaskID(ctx context.Context, taskID string) ([]domain.Trigger, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT `+triggerCols+` FROM triggers WHERE task_id = ? ORDER BY created_at`, taskID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var triggers []domain.Trigger
	for rows.Next() {
		t, err := scanTrigger(rows)
		if err != nil {
			return nil, err
		}
		triggers = append(triggers, t)
	}
	return triggers, rows.Err()
}

// Update overwrites an existing trigger.
func (r *TriggerRepository) Update(ctx context.Context, trigger domain.Trigger) error {
	res, err := r.db.ExecContext(ctx,
		`UPDATE triggers SET
			task_id = ?, type = ?, value = ?, enabled = ?,
			next_fire_at = ?, last_fired_at = ?
		 WHERE id = ?`,
		trigger.TaskID,
		string(trigger.Type),
		trigger.Value,
		boolToInt(trigger.Enabled),
		nullableTime(trigger.NextFireAt),
		nullableTime(trigger.LastFiredAt),
		trigger.ID,
	)
	if err != nil {
		return err
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		return fmt.Errorf("trigger %s: %w", trigger.ID, sql.ErrNoRows)
	}
	return nil
}

// Delete removes a trigger by ID.
func (r *TriggerRepository) Delete(ctx context.Context, id string) error {
	res, err := r.db.ExecContext(ctx, `DELETE FROM triggers WHERE id = ?`, id)
	if err != nil {
		return err
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		return fmt.Errorf("trigger %s: %w", id, sql.ErrNoRows)
	}
	return nil
}

// ListPending returns all triggers currently enabled.
//
// Decision (documented): ListPending still returns every enabled trigger, not
// only those due to fire. Incorporating NextFireAt filtering belongs to the
// future Scheduler, not the repository — changing that semantics now would
// turn the repository into a scheduler. The new columns are simply carried
// through the scan so the Scheduler can filter once it exists.
func (r *TriggerRepository) ListPending(ctx context.Context) ([]domain.Trigger, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT `+triggerCols+` FROM triggers WHERE enabled = 1 ORDER BY created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var triggers []domain.Trigger
	for rows.Next() {
		t, err := scanTrigger(rows)
		if err != nil {
			return nil, err
		}
		triggers = append(triggers, t)
	}
	return triggers, rows.Err()
}

// scanTrigger reads a single trigger row into a domain.Trigger.
func scanTrigger(s rowScanner) (domain.Trigger, error) {
	var t domain.Trigger
	var sType string
	var enabled int
	var sCreated string
	var nextFire sql.NullString
	var lastFired sql.NullString

	if err := s.Scan(
		&t.ID, &t.TaskID, &sType, &t.Value, &enabled,
		&nextFire, &lastFired, &sCreated,
	); err != nil {
		return domain.Trigger{}, err
	}

	t.Type = domain.TriggerType(sType)
	t.Enabled = enabled == 1

	var err error
	if t.NextFireAt, err = scanNullableTime(nextFire); err != nil {
		return domain.Trigger{}, err
	}
	if t.LastFiredAt, err = scanNullableTime(lastFired); err != nil {
		return domain.Trigger{}, err
	}
	if t.CreatedAt, err = scanTime(sCreated); err != nil {
		return domain.Trigger{}, err
	}
	return t, nil
}
