// Package migrations embeds the numbered SQL files in this directory so
// internal/migrate can apply them without relying on a runtime file path.
package migrations

import "embed"

//go:embed *.sql
var FS embed.FS
