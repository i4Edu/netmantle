// Package storage owns the database connection and schema migrations.
//
// Phase 1 ships a SQLite-only driver (modernc.org/sqlite — pure Go, no cgo)
// with plain-SQL migrations embedded in the binary. The schema is portable
// to PostgreSQL with minor dialect changes, which is the planned production
// target.
package storage

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

// Open opens (and pings) the database described by driver+dsn. For sqlite,
// the parent directory of a file DSN is created on demand.
func Open(driver, dsn string) (*sql.DB, error) {
	if driver != "sqlite" {
		return nil, fmt.Errorf("storage: unsupported driver %q", driver)
	}
	// modernc sqlite uses driver name "sqlite". Ensure parent dir exists for
	// file-backed databases.
	if !strings.HasPrefix(dsn, ":") && !strings.Contains(dsn, "memory") {
		if dir := filepath.Dir(dsn); dir != "" {
			if err := os.MkdirAll(dir, 0o750); err != nil {
				return nil, fmt.Errorf("storage: mkdir %s: %w", dir, err)
			}
		}
	}
	// Encourage WAL + foreign keys via DSN parameters.
	dsnFull := dsn
	if !strings.Contains(dsn, "?") {
		params := url.Values{}
		params.Set("_pragma", "journal_mode(WAL)")
		params.Add("_pragma", "foreign_keys(ON)")
		params.Add("_pragma", "busy_timeout(5000)")
		dsnFull = dsn + "?" + params.Encode()
	}

	db, err := sql.Open("sqlite", dsnFull)
	if err != nil {
		return nil, fmt.Errorf("storage: open: %w", err)
	}
	// SQLite is happiest with a single writer; keep the pool small.
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("storage: ping: %w", err)
	}
	return db, nil
}

// Migrate applies any unapplied embedded migrations in version order.
func Migrate(ctx context.Context, db *sql.DB) error {
	if _, err := db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
        version INTEGER PRIMARY KEY,
        applied_at TEXT NOT NULL
    )`); err != nil {
		return fmt.Errorf("storage: bootstrap migrations table: %w", err)
	}

	entries, err := migrationsFS.ReadDir("migrations")
	if err != nil {
		return fmt.Errorf("storage: read migrations: %w", err)
	}
	type mig struct {
		version int
		name    string
	}
	var all []mig
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".sql") {
			continue
		}
		var v int
		if _, err := fmt.Sscanf(e.Name(), "%04d_", &v); err != nil {
			return fmt.Errorf("storage: bad migration filename %q", e.Name())
		}
		all = append(all, mig{version: v, name: e.Name()})
	}
	sort.Slice(all, func(i, j int) bool { return all[i].version < all[j].version })

	applied := map[int]bool{}
	rows, err := db.QueryContext(ctx, `SELECT version FROM schema_migrations`)
	if err != nil {
		return fmt.Errorf("storage: list applied: %w", err)
	}
	for rows.Next() {
		var v int
		if err := rows.Scan(&v); err != nil {
			rows.Close()
			return err
		}
		applied[v] = true
	}
	rows.Close()

	for _, m := range all {
		if applied[m.version] {
			continue
		}
		body, err := migrationsFS.ReadFile("migrations/" + m.name)
		if err != nil {
			return err
		}
		// SQLite cannot run multiple statements in one Exec; split on ';'
		// at top level. Migrations here use only simple statements.
		for _, stmt := range splitStatements(string(body)) {
			if strings.TrimSpace(stmt) == "" {
				continue
			}
			if _, err := db.ExecContext(ctx, stmt); err != nil {
				return fmt.Errorf("storage: migration %s: %w", m.name, err)
			}
		}
		if _, err := db.ExecContext(ctx,
			`INSERT INTO schema_migrations(version, applied_at) VALUES(?, ?)`,
			m.version, time.Now().UTC().Format(time.RFC3339)); err != nil {
			return fmt.Errorf("storage: record migration %s: %w", m.name, err)
		}
	}
	return nil
}

// splitStatements splits a SQL script on top-level semicolons, ignoring
// any `;` that appears inside line comments (`-- ...`) or string literals
// (`'...'`). Sufficient for the SQLite migrations we hand-write.
func splitStatements(script string) []string {
	var (
		out      []string
		buf      strings.Builder
		inString bool
		inLine   bool
	)
	for i := 0; i < len(script); i++ {
		c := script[i]
		if inLine {
			if c == '\n' {
				inLine = false
			}
			buf.WriteByte(c)
			continue
		}
		if inString {
			if c == '\'' {
				// Handle escaped '' inside string literal.
				if i+1 < len(script) && script[i+1] == '\'' {
					buf.WriteByte(c)
					buf.WriteByte(script[i+1])
					i++
					continue
				}
				inString = false
			}
			buf.WriteByte(c)
			continue
		}
		if c == '-' && i+1 < len(script) && script[i+1] == '-' {
			inLine = true
			buf.WriteByte(c)
			continue
		}
		if c == '\'' {
			inString = true
			buf.WriteByte(c)
			continue
		}
		if c == ';' {
			out = append(out, buf.String())
			buf.Reset()
			continue
		}
		buf.WriteByte(c)
	}
	if strings.TrimSpace(buf.String()) != "" {
		out = append(out, buf.String())
	}
	return out
}
