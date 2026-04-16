// Package ids generates UUIDv7 identifiers for domain rows.
// UUIDv7 ids are lexicographically time-ordered which makes them
// index-friendly in both SQLite and Postgres.
package ids

import "github.com/google/uuid"

// New returns a new UUIDv7 as its canonical string form.
func New() string {
	u, err := uuid.NewV7()
	if err != nil {
		// NewV7 only fails on crypto/rand failure, which is a fatal system
		// condition. Convert to a panic so we don't return malformed ids.
		panic("ids.New: uuid.NewV7 failed: " + err.Error())
	}
	return u.String()
}
