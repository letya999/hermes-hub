// Package migrations embeds the numbered SQL schema of spawn-controller.db.
package migrations

import "embed"

//go:embed *.sql
var FS embed.FS
