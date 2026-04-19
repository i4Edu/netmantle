// Package tenants exposes tenant CRUD and per-tenant quotas (Phase 9).
package tenants

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// ErrQuotaExceeded is returned when a write would breach a tenant's quota.
var ErrQuotaExceeded = errors.New("tenants: quota exceeded")

// Tenant is a multi-tenancy unit.
type Tenant struct {
	ID         int64     `json:"id"`
	Name       string    `json:"name"`
	MaxDevices int       `json:"max_devices"` // 0 = unlimited
	CreatedAt  time.Time `json:"created_at"`
}

// Service owns tenant CRUD and quota enforcement.
type Service struct{ DB *sql.DB }

// New constructs a Service.
func New(db *sql.DB) *Service { return &Service{DB: db} }

// Create inserts a tenant and its quota row inside a single transaction
// so a failed quota insert never leaves an orphan tenant row.
func (s *Service) Create(ctx context.Context, name string, maxDevices int) (Tenant, error) {
	if name == "" {
		return Tenant{}, errors.New("tenants: name required")
	}
	now := time.Now().UTC()
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return Tenant{}, err
	}
	defer func() { _ = tx.Rollback() }()

	res, err := tx.ExecContext(ctx, `INSERT INTO tenants(name, created_at) VALUES(?, ?)`, name, now.Format(time.RFC3339))
	if err != nil {
		return Tenant{}, err
	}
	id, _ := res.LastInsertId()
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO tenant_quotas(tenant_id, max_devices) VALUES(?, ?)`, id, maxDevices); err != nil {
		return Tenant{}, err
	}
	if err := tx.Commit(); err != nil {
		return Tenant{}, err
	}
	return Tenant{ID: id, Name: name, MaxDevices: maxDevices, CreatedAt: now}, nil
}

// List returns all tenants.
func (s *Service) List(ctx context.Context) ([]Tenant, error) {
	rows, err := s.DB.QueryContext(ctx, `
        SELECT t.id, t.name, IFNULL(q.max_devices, 0), t.created_at
        FROM tenants t LEFT JOIN tenant_quotas q ON q.tenant_id = t.id
        ORDER BY t.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Tenant
	for rows.Next() {
		var (
			t  Tenant
			ts string
		)
		if err := rows.Scan(&t.ID, &t.Name, &t.MaxDevices, &ts); err != nil {
			return nil, err
		}
		t.CreatedAt, _ = time.Parse(time.RFC3339, ts)
		out = append(out, t)
	}
	return out, rows.Err()
}

// SetQuota updates a tenant's max_devices.
func (s *Service) SetQuota(ctx context.Context, tenantID int64, maxDevices int) error {
	_, err := s.DB.ExecContext(ctx, `
        INSERT INTO tenant_quotas(tenant_id, max_devices) VALUES(?, ?)
        ON CONFLICT(tenant_id) DO UPDATE SET max_devices = excluded.max_devices`,
		tenantID, maxDevices)
	return err
}

// CheckDeviceQuota returns ErrQuotaExceeded if adding one more device
// would exceed the configured maximum.
func (s *Service) CheckDeviceQuota(ctx context.Context, tenantID int64) error {
	var max int
	if err := s.DB.QueryRowContext(ctx,
		`SELECT IFNULL(max_devices, 0) FROM tenant_quotas WHERE tenant_id=?`, tenantID,
	).Scan(&max); err != nil && err != sql.ErrNoRows {
		return err
	}
	if max == 0 {
		return nil
	}
	var have int
	if err := s.DB.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM devices WHERE tenant_id=?`, tenantID).Scan(&have); err != nil {
		return err
	}
	if have >= max {
		return fmt.Errorf("%w: %d/%d devices", ErrQuotaExceeded, have, max)
	}
	return nil
}
