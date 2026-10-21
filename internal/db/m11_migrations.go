// M11 migrations: per-host agent request-signing key.
//
// hosts.sign_key holds the HMAC key issued at enrollment when the agent
// advertises signing support ("" = legacy token-only host). The column
// is added with a portable ALTER (runs once, tracked by
// schema_migrations like every other migration in this package).

package db

func init() { migrations = append(migrations, m11Migrations...) }

var m11Migrations = []string{
	`ALTER TABLE hosts ADD COLUMN sign_key TEXT NOT NULL DEFAULT ''`,
}
