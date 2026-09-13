// Package migrations embeds the SQL schema files so the binary carries its own schema.
package migrations

import "embed"

//go:embed *.sql
var FS embed.FS
