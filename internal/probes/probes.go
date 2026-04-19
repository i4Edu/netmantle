// Package probes implements runtime-state probes (Phase 8): scheduled
// driver-defined commands whose output is captured as time-series rows.
// Probe results can be evaluated against compliance rules to power runtime
// compliance dashboards.
package probes

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// Probe is the user-defined definition.
type Probe struct {
	ID        int64  `json:"id"`
	TenantID  int64  `json:"tenant_id"`
	Name      string `json:"name"`
	Command   string `json:"command"`
	IntervalS int    `json:"interval_s"`
}

// Run is one execution of a probe against one device.
type Run struct {
	ID        int64     `json:"id"`
	ProbeID   int64     `json:"probe_id"`
	DeviceID  int64     `json:"device_id"`
	Output    string    `json:"output,omitempty"`
	Error     string    `json:"error,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

// Service owns probes CRUD and run history.
type Service struct {
	DB *sql.DB
}

// New constructs a Service.
func New(db *sql.DB) *Service { return &Service{DB: db} }

// Create inserts a probe.
func (s *Service) Create(ctx context.Context, p Probe) (Probe, error) {
	if p.Name == "" || p.Command == "" {
		return Probe{}, errors.New("probes: name and command required")
	}
	if p.IntervalS <= 0 {
		p.IntervalS = 300
	}
	res, err := s.DB.ExecContext(ctx,
		`INSERT INTO probes(tenant_id, name, command, interval_s, created_at) VALUES(?, ?, ?, ?, ?)`,
		p.TenantID, p.Name, p.Command, p.IntervalS, time.Now().UTC().Format(time.RFC3339))
	if err != nil {
		return Probe{}, err
	}
	id, _ := res.LastInsertId()
	p.ID = id
	return p, nil
}

// List returns probes for a tenant.
func (s *Service) List(ctx context.Context, tenantID int64) ([]Probe, error) {
	rows, err := s.DB.QueryContext(ctx,
		`SELECT id, tenant_id, name, command, interval_s FROM probes WHERE tenant_id=? ORDER BY name`, tenantID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Probe
	for rows.Next() {
		var p Probe
		if err := rows.Scan(&p.ID, &p.TenantID, &p.Name, &p.Command, &p.IntervalS); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// Delete removes a probe.
func (s *Service) Delete(ctx context.Context, tenantID, id int64) error {
	_, err := s.DB.ExecContext(ctx, `DELETE FROM probes WHERE tenant_id=? AND id=?`, tenantID, id)
	return err
}

// RecordRun stores a probe-run row.
func (s *Service) RecordRun(ctx context.Context, probeID, deviceID int64, output string, runErr error) error {
	var es string
	if runErr != nil {
		es = runErr.Error()
	}
	_, err := s.DB.ExecContext(ctx,
		`INSERT INTO probe_runs(probe_id, device_id, output, error, created_at) VALUES(?, ?, ?, ?, ?)`,
		probeID, deviceID, output, es, time.Now().UTC().Format(time.RFC3339))
	return err
}

// LatestRuns returns the most recent N runs for a probe.
func (s *Service) LatestRuns(ctx context.Context, probeID int64, limit int) ([]Run, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := s.DB.QueryContext(ctx,
		`SELECT id, probe_id, device_id, IFNULL(output,''), IFNULL(error,''), created_at FROM probe_runs WHERE probe_id=? ORDER BY created_at DESC LIMIT ?`,
		probeID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Run
	for rows.Next() {
		var (
			r  Run
			ts string
		)
		if err := rows.Scan(&r.ID, &r.ProbeID, &r.DeviceID, &r.Output, &r.Error, &ts); err != nil {
			return nil, err
		}
		r.CreatedAt, _ = time.Parse(time.RFC3339, ts)
		out = append(out, r)
	}
	return out, rows.Err()
}

// PruneOlderThan deletes probe runs older than t (retention policy hook).
func (s *Service) PruneOlderThan(ctx context.Context, t time.Time) (int64, error) {
	r, err := s.DB.ExecContext(ctx, `DELETE FROM probe_runs WHERE created_at < ?`, t.UTC().Format(time.RFC3339))
	if err != nil {
		return 0, err
	}
	n, _ := r.RowsAffected()
	return n, nil
}
