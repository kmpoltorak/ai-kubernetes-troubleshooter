// Package migrations embeds the SQL migration files into the binary.
// Files are named NNNN_name.up.sql and NNNN_name.down.sql.
package migrations

import "embed"

//go:embed *.sql
var FS embed.FS
