// Package migrations embeds the SQL migration files so the server binary can
// apply them at boot without external files. Files follow the golang-migrate
// naming convention: {version}_{title}.{up|down}.sql.
package migrations

import "embed"

//go:embed *.sql
var FS embed.FS
