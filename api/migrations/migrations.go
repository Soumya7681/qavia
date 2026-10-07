// Package migrations embeds the goose SQL files.
//
// A deploy carries its own migrations, so there is no separate migration image to
// keep in step with the binary (tech-stack.md 5).
package migrations

import "embed"

// FS holds every checked-in migration.
//
//go:embed *.sql
var FS embed.FS
