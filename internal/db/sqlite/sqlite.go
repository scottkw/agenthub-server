// Package sqlite implements the db.DB interface using modernc.org/sqlite
// (pure-Go SQLite driver, no CGo).
package sqlite

import (
	"context"
	"database/sql"
	"fmt"

	_ "modernc.org/sqlite"
)

type Options struct {
	// Path is the file path (or ":memory:"). Directory must exist.
	Path string
	// MaxOpenConns defaults to 1 if zero (SQLite is single-writer).
	MaxOpenConns int
}

type db struct {
	sql *sql.DB
}

func Open(opts Options) (*db, error) {
	if opts.Path == "" {
		return nil, fmt.Errorf("sqlite.Open: Path is required")
	}
	if opts.MaxOpenConns == 0 {
		opts.MaxOpenConns = 1
	}

	// Configure via DSN query params: WAL + foreign keys + busy timeout.
	dsn := opts.Path + "?_pragma=journal_mode(WAL)" +
		"&_pragma=foreign_keys(1)" +
		"&_pragma=busy_timeout(5000)" +
		"&_pragma=synchronous(NORMAL)"

	sqlDB, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("sqlite open: %w", err)
	}
	sqlDB.SetMaxOpenConns(opts.MaxOpenConns)

	if err := sqlDB.PingContext(context.Background()); err != nil {
		_ = sqlDB.Close()
		return nil, fmt.Errorf("sqlite ping: %w", err)
	}

	return &db{sql: sqlDB}, nil
}

func (d *db) SQL() *sql.DB                   { return d.sql }
func (d *db) Driver() string                 { return "sqlite" }
func (d *db) Ping(ctx context.Context) error { return d.sql.PingContext(ctx) }
func (d *db) Close() error                   { return d.sql.Close() }
