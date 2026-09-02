// Package migrations embeds this directory's SQL files so they travel
// inside the compiled binary rather than depending on a filesystem checkout
// being present at runtime.
//
// This file lives in migrations/ itself, and internal/adapters/postgres
// imports the package, rather than the other way around (an embed directive
// declared inside the adapter package pointing back up at migrations/):
// Go's //go:embed can only reach files under the directory tree rooted at
// the file that declares it, never via a ".." parent path, so the directive
// has to live here. Keeping the SQL at the repository's conventional
// migrations/ path — rather than duplicating it under the adapter package
// so the embed directive could stay there — is what lets psql, a future
// migration CLI, and a reviewer scanning the repo tree all find "the
// migrations" in the one place every one of them expects it.
package migrations

import "embed"

// FS holds every *.up.sql and *.down.sql file in this directory.
//
//go:embed *.sql
var FS embed.FS
