// Package devices owns device, device-group, and tenant data access.
package devices

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Device is the canonical inventory record.
type Device struct {
	ID           int64     `json:"id"`
	TenantID     int64     `json:"tenant_id"`
	Hostname     string    `json:"hostname"`
	Address      string    `json:"address"`
	Port         int       `json:"port"`
	Driver       string    `json:"driver"`
	GroupID      *int64    `json:"group_id,omitempty"`
	CredentialID *int64    `json:"credential_id,omitempty"`
	CreatedAt    time.Time `json:"created_at"`
}

// Group is a logical bucket of devices, used for scheduling and RBAC.
type Group struct {
	ID        int64     `json:"id"`
	TenantID  int64     `json:"tenant_id"`
	Name      string    `json:"name"`
	CreatedAt time.Time `json:"created_at"`
}

// Repo wraps the *sql.DB with typed CRUD helpers.
type Repo struct{ DB *sql.DB }

// NewRepo constructs a Repo.
func NewRepo(db *sql.DB) *Repo { return &Repo{DB: db} }

// ErrNotFound is returned when a lookup yields zero rows.
var ErrNotFound = errors.New("devices: not found")

// CreateDevice inserts a device.
func (r *Repo) CreateDevice(ctx context.Context, d Device) (Device, error) {
	if err := validateDevice(d); err != nil {
		return Device{}, err
	}
	now := time.Now().UTC()
	res, err := r.DB.ExecContext(ctx, `
        INSERT INTO devices(tenant_id, hostname, address, port, driver, group_id, credential_id, created_at)
        VALUES(?, ?, ?, ?, ?, ?, ?, ?)`,
		d.TenantID, d.Hostname, d.Address, d.Port, d.Driver, d.GroupID, d.CredentialID, now.Format(time.RFC3339))
	if err != nil {
		return Device{}, fmt.Errorf("devices: insert: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return Device{}, err
	}
	d.ID = id
	d.CreatedAt = now
	return d, nil
}

// GetDevice returns a device by ID, scoped to the tenant.
func (r *Repo) GetDevice(ctx context.Context, tenantID, id int64) (Device, error) {
	var (
		d         Device
		createdAt string
	)
	err := r.DB.QueryRowContext(ctx, `
        SELECT id, tenant_id, hostname, address, port, driver, group_id, credential_id, created_at
        FROM devices WHERE tenant_id = ? AND id = ?`, tenantID, id,
	).Scan(&d.ID, &d.TenantID, &d.Hostname, &d.Address, &d.Port, &d.Driver, &d.GroupID, &d.CredentialID, &createdAt)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Device{}, ErrNotFound
		}
		return Device{}, err
	}
	d.CreatedAt, _ = time.Parse(time.RFC3339, createdAt)
	return d, nil
}

// ListDevices returns all devices for a tenant, hostname-sorted.
func (r *Repo) ListDevices(ctx context.Context, tenantID int64) ([]Device, error) {
	rows, err := r.DB.QueryContext(ctx, `
        SELECT id, tenant_id, hostname, address, port, driver, group_id, credential_id, created_at
        FROM devices WHERE tenant_id = ? ORDER BY hostname ASC`, tenantID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Device
	for rows.Next() {
		var (
			d  Device
			ts string
		)
		if err := rows.Scan(&d.ID, &d.TenantID, &d.Hostname, &d.Address, &d.Port, &d.Driver, &d.GroupID, &d.CredentialID, &ts); err != nil {
			return nil, err
		}
		d.CreatedAt, _ = time.Parse(time.RFC3339, ts)
		out = append(out, d)
	}
	return out, rows.Err()
}

// UpdateDevice updates the mutable fields of a device.
func (r *Repo) UpdateDevice(ctx context.Context, d Device) (Device, error) {
	if err := validateDevice(d); err != nil {
		return Device{}, err
	}
	res, err := r.DB.ExecContext(ctx, `
        UPDATE devices SET hostname=?, address=?, port=?, driver=?, group_id=?, credential_id=?
        WHERE tenant_id=? AND id=?`,
		d.Hostname, d.Address, d.Port, d.Driver, d.GroupID, d.CredentialID, d.TenantID, d.ID)
	if err != nil {
		return Device{}, err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return Device{}, ErrNotFound
	}
	return r.GetDevice(ctx, d.TenantID, d.ID)
}

// DeleteDevice removes a device.
func (r *Repo) DeleteDevice(ctx context.Context, tenantID, id int64) error {
	res, err := r.DB.ExecContext(ctx, `DELETE FROM devices WHERE tenant_id=? AND id=?`, tenantID, id)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// CreateGroup inserts a device group.
func (r *Repo) CreateGroup(ctx context.Context, g Group) (Group, error) {
	if strings.TrimSpace(g.Name) == "" {
		return Group{}, errors.New("devices: group name required")
	}
	now := time.Now().UTC()
	res, err := r.DB.ExecContext(ctx,
		`INSERT INTO device_groups(tenant_id, name, created_at) VALUES(?, ?, ?)`,
		g.TenantID, g.Name, now.Format(time.RFC3339))
	if err != nil {
		return Group{}, err
	}
	id, _ := res.LastInsertId()
	g.ID = id
	g.CreatedAt = now
	return g, nil
}

// ListGroups returns all groups for a tenant.
func (r *Repo) ListGroups(ctx context.Context, tenantID int64) ([]Group, error) {
	rows, err := r.DB.QueryContext(ctx,
		`SELECT id, tenant_id, name, created_at FROM device_groups WHERE tenant_id = ? ORDER BY name ASC`, tenantID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Group
	for rows.Next() {
		var (
			g  Group
			ts string
		)
		if err := rows.Scan(&g.ID, &g.TenantID, &g.Name, &ts); err != nil {
			return nil, err
		}
		g.CreatedAt, _ = time.Parse(time.RFC3339, ts)
		out = append(out, g)
	}
	return out, rows.Err()
}

func validateDevice(d Device) error {
	if d.TenantID <= 0 {
		return errors.New("devices: tenant_id required")
	}
	if strings.TrimSpace(d.Hostname) == "" {
		return errors.New("devices: hostname required")
	}
	if strings.TrimSpace(d.Address) == "" {
		return errors.New("devices: address required")
	}
	if d.Port <= 0 || d.Port > 65535 {
		return errors.New("devices: port out of range")
	}
	if strings.TrimSpace(d.Driver) == "" {
		return errors.New("devices: driver required")
	}
	return nil
}
