// Package sqlite implements persistence for the alter domain using SQLite.
//
// It is deliberately decoupled from internal/domain: the domain only defines
// interfaces (ports), and this package provides concrete adapters. Swapping
// SQLite for PostgreSQL later only requires a new adapter package.
package sqlite

import (
	"database/sql"
	"fmt"

	_ "modernc.org/sqlite" // pure-Go SQLite driver (no CGO)
)

// Store owns the SQLite connection and runs migrations on open.
type Store struct {
	db *sql.DB
}

// Open opens (or creates) the SQLite database at path and applies migrations.
func Open(path string) (*Store, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}

	if err := db.Ping(); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("ping sqlite: %w", err)
	}

	if _, err := db.Exec("PRAGMA journal_mode=WAL;"); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("set WAL: %w", err)
	}
	if _, err := db.Exec("PRAGMA foreign_keys=ON;"); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("enable foreign keys: %w", err)
	}

	if err := migrate(db); err != nil {
		_ = db.Close()
		return nil, err
	}

	return &Store{db: db}, nil
}

// Close releases the database connection.
func (s *Store) Close() error {
	return s.db.Close()
}

// NewTaskRepository returns a SQLite-backed domain.TaskRepository.
func (s *Store) NewTaskRepository() *TaskRepository {
	return &TaskRepository{db: s.db}
}

// NewTriggerRepository returns a SQLite-backed domain.TriggerRepository.
func (s *Store) NewTriggerRepository() *TriggerRepository {
	return &TriggerRepository{db: s.db}
}

// NewEventStore returns a SQLite-backed domain.EventStore.
func (s *Store) NewEventStore() *EventStore {
	return &EventStore{db: s.db}
}
