package sqlite

import (
	"database/sql"
	"time"
)

// TimeLayout is the canonical format used for all timestamps in SQLite.
// RFC3339Nano keeps full precision and is trivially portable to PostgreSQL.
const TimeLayout = time.RFC3339Nano

// formatTime encodes a time.Time as an RFC3339Nano string.
func formatTime(t time.Time) string {
	return t.UTC().Format(TimeLayout)
}

// nullableTime encodes an optional time.Time for storage (NULL when nil).
func nullableTime(t *time.Time) any {
	if t == nil {
		return nil
	}
	return formatTime(*t)
}

// scanTime decodes a stored timestamp string back into time.Time.
func scanTime(s string) (time.Time, error) {
	return time.Parse(TimeLayout, s)
}

// boolToInt encodes a bool for storage (SQLite has no native bool).
func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// scanNullableTime decodes an optional timestamp column (NULL -> nil).
func scanNullableTime(ns sql.NullString) (*time.Time, error) {
	if !ns.Valid {
		return nil, nil
	}
	t, err := scanTime(ns.String)
	if err != nil {
		return nil, err
	}
	return &t, nil
}
