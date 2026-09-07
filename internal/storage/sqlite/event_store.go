package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"

	"github.com/verdu/alter/internal/domain"
)

// EventStore is a SQLite-backed domain.EventStore.
type EventStore struct {
	db *sql.DB
}

var _ domain.EventStore = (*EventStore)(nil)

// Save persists an event. Payload is encoded as JSON text.
func (s *EventStore) Save(ctx context.Context, event domain.Event) error {
	payload, err := json.Marshal(event.Payload)
	if err != nil {
		return err
	}

	_, err = s.db.ExecContext(ctx,
		`INSERT INTO events (id, type, payload, created_at) VALUES (?,?,?,?)`,
		event.ID,
		string(event.Type),
		string(payload),
		formatTime(event.CreatedAt),
	)
	return err
}

// ListByType returns all events of a given type, oldest first.
func (s *EventStore) ListByType(ctx context.Context, eventType domain.EventType) ([]domain.Event, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, type, payload, created_at FROM events WHERE type = ? ORDER BY created_at`,
		string(eventType))
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var events []domain.Event
	for rows.Next() {
		var e domain.Event
		var sType string
		var payload string
		var sCreated string

		if err := rows.Scan(&e.ID, &sType, &payload, &sCreated); err != nil {
			return nil, err
		}

		e.Type = domain.EventType(sType)
		if err := json.Unmarshal([]byte(payload), &e.Payload); err != nil {
			return nil, err
		}
		if e.CreatedAt, err = scanTime(sCreated); err != nil {
			return nil, err
		}

		events = append(events, e)
	}
	return events, rows.Err()
}
