// Package migrations embeds the numbered schema migrations (NNNN_name.sql).
package migrations

import "embed"

// FS holds all *.sql migrations.
//
//go:embed *.sql
var FS embed.FS
