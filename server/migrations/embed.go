package migrations

import "embed"

// FS embeds all SQL migration files in the migrations directory.
//
//go:embed *.sql
var FS embed.FS
