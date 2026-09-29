package migrations

import "embed"

// Files contains the controller's private database migrations.
//
//go:embed *.sql
var Files embed.FS
