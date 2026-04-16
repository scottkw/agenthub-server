// Package migrations applies schema migrations using pressly/goose against
// a dialect-specific embedded SQL directory.
package migrations

import (
	"context"
	"embed"
	"fmt"

	"github.com/pressly/goose/v3"

	"github.com/scottkw/agenthub-server/internal/db"
)

//go:embed sqlite/*.sql
var sqliteFS embed.FS

// Apply runs all pending migrations for d.Driver() against the database.
func Apply(ctx context.Context, d db.DB) error {
	switch d.Driver() {
	case "sqlite":
		goose.SetBaseFS(sqliteFS)
		if err := goose.SetDialect("sqlite3"); err != nil {
			return fmt.Errorf("set dialect: %w", err)
		}
		return goose.UpContext(ctx, d.SQL(), "sqlite")
	default:
		return fmt.Errorf("migrations.Apply: unsupported driver %q", d.Driver())
	}
}
