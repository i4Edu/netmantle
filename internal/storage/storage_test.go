package storage

import (
	"context"
	"path/filepath"
	"testing"
)

func TestOpenAndMigrate(t *testing.T) {
	dir := t.TempDir()
	db, err := Open("sqlite", filepath.Join(dir, "t.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()
	if err := Migrate(context.Background(), db); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	// Idempotent.
	if err := Migrate(context.Background(), db); err != nil {
		t.Fatalf("migrate2: %v", err)
	}
	// Sanity: tables exist.
	for _, table := range []string{"users", "tenants", "devices", "credentials", "device_groups", "backup_runs", "config_versions", "audit_log", "sessions"} {
		var n int
		if err := db.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&n); err != nil {
			t.Errorf("table %s: %v", table, err)
		}
	}
}

func TestUnsupportedDriver(t *testing.T) {
	if _, err := Open("mysql", "x"); err == nil {
		t.Fatal("expected error")
	}
}
