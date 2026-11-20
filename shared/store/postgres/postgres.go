// Package postgres links the pgx database/sql driver (ADR-0115). Services
// that accept postgres:// URLs import it for side effects; SQLite-only
// consumers never link pgx.
package postgres

import _ "github.com/jackc/pgx/v5/stdlib"
