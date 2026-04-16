// Package db defines the storage interface implemented by sqlite and postgres.
package db

import (
	"context"
	"database/sql"
)

// DB is the database handle abstraction used by the rest of the server.
// Implementations wrap database/sql with driver-specific setup (pragmas, schema, etc).
type DB interface {
	// SQL returns the underlying *sql.DB for query execution.
	SQL() *sql.DB

	// Driver returns a short identifier: "sqlite" or "postgres".
	Driver() string

	// Ping verifies the connection is healthy.
	Ping(ctx context.Context) error

	// Close releases all resources.
	Close() error
}
