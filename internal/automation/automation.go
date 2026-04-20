// Package automation implements push jobs (Phase 6).
//
// A push job carries a Go text/template that is rendered per device with
// the device's metadata and a user-supplied variable map. The Preview API
// returns the rendered config without executing it; Run actually pushes
// to each device, with smart grouping of identical results in the result
// view.
package automation

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"text/template"
	"time"

	"github.com/i4Edu/netmantle/internal/audit"
	"github.com/i4Edu/netmantle/internal/devices"
	"github.com/i4Edu/netmantle/internal/drivers"
)

// Job is a push job definition.
type Job struct {
	ID            int64             `json:"id"`
	TenantID      int64             `json:"tenant_id"`
	Name          string            `json:"name"`
	Template      string            `json:"template"`
	Variables     map[string]string `json:"variables,omitempty"`
	TargetGroupID *int64            `json:"target_group_id,omitempty"`
}

// Render expands a template with device + variables.
type renderCtx struct {
	Device devices.Device
	Vars   map[string]string
}

// Result is one device's outcome.
type Result struct {
	DeviceID int64  `json:"device_id"`
	Hostname string `json:"hostname"`
	Rendered string `json:"rendered"`
	Status   string `json:"status"`
	Output   string `json:"output,omitempty"`
	Error    string `json:"error,omitempty"`
	Hash     string `json:"hash,omitempty"` // sha256 of rendered for grouping
}

// Group bundles identical results.
type Group struct {
	Hash     string   `json:"hash"`
	Rendered string   `json:"rendered"`
	Status   string   `json:"status"`
	Devices  []string `json:"devices"`
}

// Service owns Job CRUD + execution.
type Service struct {
	DB      *sql.DB
	Devices *devices.Repo
	// PreFlight validates a target is reachable and has credentials before
	// a live push is attempted.
	PreFlight func(ctx context.Context, d devices.Device) error
	// Audit, when set, records high-priority rollback scaffold events.
	Audit *audit.Service
	// Executor pushes a rendered config to a device. Returns combined output
	// or an error. In production it wraps SSH transport + driver.Apply.
	Executor func(ctx context.Context, d devices.Device, config string) (string, error)
}

// New constructs a Service.
func New(db *sql.DB, dr *devices.Repo, exec func(ctx context.Context, d devices.Device, config string) (string, error)) *Service {
	return &Service{DB: db, Devices: dr, Executor: exec}
}

// CreateJob inserts a Job.
func (s *Service) CreateJob(ctx context.Context, j Job) (Job, error) {
	if j.Name == "" || j.Template == "" {
		return Job{}, errors.New("automation: name and template required")
	}
	if _, err := template.New("validate").Parse(j.Template); err != nil {
		return Job{}, fmt.Errorf("automation: invalid template: %w", err)
	}
	varsBytes, _ := json.Marshal(j.Variables)
	res, err := s.DB.ExecContext(ctx,
		`INSERT INTO push_jobs(tenant_id, name, template, variables, target_group_id, created_at) VALUES(?, ?, ?, ?, ?, ?)`,
		j.TenantID, j.Name, j.Template, string(varsBytes), j.TargetGroupID,
		time.Now().UTC().Format(time.RFC3339))
	if err != nil {
		return Job{}, err
	}
	id, _ := res.LastInsertId()
	j.ID = id
	return j, nil
}

// GetJob fetches a Job.
func (s *Service) GetJob(ctx context.Context, tenantID, id int64) (Job, error) {
	var (
		j        Job
		varsJSON string
		gid      sql.NullInt64
	)
	err := s.DB.QueryRowContext(ctx,
		`SELECT id, tenant_id, name, template, variables, target_group_id FROM push_jobs WHERE tenant_id=? AND id=?`,
		tenantID, id,
	).Scan(&j.ID, &j.TenantID, &j.Name, &j.Template, &varsJSON, &gid)
	if err != nil {
		return Job{}, err
	}
	if varsJSON != "" {
		_ = json.Unmarshal([]byte(varsJSON), &j.Variables)
	}
	if gid.Valid {
		v := gid.Int64
		j.TargetGroupID = &v
	}
	return j, nil
}

// ListJobs returns all jobs for a tenant.
func (s *Service) ListJobs(ctx context.Context, tenantID int64) ([]Job, error) {
	rows, err := s.DB.QueryContext(ctx,
		`SELECT id, tenant_id, name, template, variables, target_group_id FROM push_jobs WHERE tenant_id=? ORDER BY name`,
		tenantID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Job
	for rows.Next() {
		var (
			j        Job
			varsJSON string
			gid      sql.NullInt64
		)
		if err := rows.Scan(&j.ID, &j.TenantID, &j.Name, &j.Template, &varsJSON, &gid); err != nil {
			return nil, err
		}
		if varsJSON != "" {
			_ = json.Unmarshal([]byte(varsJSON), &j.Variables)
		}
		if gid.Valid {
			v := gid.Int64
			j.TargetGroupID = &v
		}
		out = append(out, j)
	}
	return out, rows.Err()
}

// Preview renders the template for each targeted device without executing.
func (s *Service) Preview(ctx context.Context, tenantID, jobID int64) ([]Result, error) {
	j, err := s.GetJob(ctx, tenantID, jobID)
	if err != nil {
		return nil, err
	}
	devs, err := s.targets(ctx, tenantID, j)
	if err != nil {
		return nil, err
	}
	tmpl, err := template.New("push").Parse(j.Template)
	if err != nil {
		return nil, err
	}
	out := make([]Result, 0, len(devs))
	for _, d := range devs {
		var b strings.Builder
		if err := tmpl.Execute(&b, renderCtx{Device: d, Vars: j.Variables}); err != nil {
			out = append(out, Result{DeviceID: d.ID, Hostname: d.Hostname, Status: "render_error", Error: err.Error()})
			continue
		}
		rendered := b.String()
		out = append(out, Result{
			DeviceID: d.ID, Hostname: d.Hostname,
			Rendered: rendered, Status: "preview", Hash: hashOf(rendered),
		})
	}
	return out, nil
}

// Run executes a job: renders + applies to each targeted device,
// concurrency-bounded, persisting results.
func (s *Service) Run(ctx context.Context, tenantID, jobID, concurrency int64) ([]Result, error) {
	j, err := s.GetJob(ctx, tenantID, jobID)
	if err != nil {
		return nil, err
	}
	devs, err := s.targets(ctx, tenantID, j)
	if err != nil {
		return nil, err
	}
	tmpl, err := template.New("push").Parse(j.Template)
	if err != nil {
		return nil, err
	}
	if concurrency <= 0 {
		concurrency = 4
	}
	now := time.Now().UTC()
	res, err := s.DB.ExecContext(ctx,
		`INSERT INTO push_runs(job_id, started_at, status) VALUES(?, ?, 'running')`,
		j.ID, now.Format(time.RFC3339))
	if err != nil {
		return nil, err
	}
	runID, _ := res.LastInsertId()

	sem := make(chan struct{}, concurrency)
	var (
		mu  sync.Mutex
		wg  sync.WaitGroup
		out = make([]Result, 0, len(devs))
	)
	for _, d := range devs {
		d := d
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			r := Result{DeviceID: d.ID, Hostname: d.Hostname}
			var b strings.Builder
			if err := tmpl.Execute(&b, renderCtx{Device: d, Vars: j.Variables}); err != nil {
				r.Status = "render_error"
				r.Error = err.Error()
				mu.Lock()
				out = append(out, r)
				mu.Unlock()
				return
			}
			r.Rendered = b.String()
			r.Hash = hashOf(r.Rendered)
			if s.PreFlight != nil {
				if err := s.PreFlight(ctx, d); err != nil {
					r.Status = "failed"
					r.Error = fmt.Sprintf("pre-flight check failed: %v", err)
					s.recordHighPriorityRollbackScaffold(ctx, tenantID, d, r.Error)
					mu.Lock()
					out = append(out, r)
					mu.Unlock()
					return
				}
			}
			if s.Executor == nil {
				r.Status = "skipped"
			} else {
				output, err := s.Executor(ctx, d, r.Rendered)
				r.Output = output
				if err != nil {
					r.Status = "failed"
					r.Error = err.Error()
					s.recordHighPriorityRollbackScaffold(ctx, tenantID, d, err.Error())
				} else {
					r.Status = "applied"
				}
			}
			mu.Lock()
			out = append(out, r)
			mu.Unlock()
		}()
	}
	wg.Wait()

	for _, r := range out {
		_, _ = s.DB.ExecContext(ctx,
			`INSERT INTO push_results(run_id, device_id, rendered, status, output, error) VALUES(?, ?, ?, ?, ?, ?)`,
			runID, r.DeviceID, r.Rendered, r.Status, nullable(r.Output), nullable(r.Error))
	}
	_, _ = s.DB.ExecContext(ctx,
		`UPDATE push_runs SET finished_at=?, status=? WHERE id=?`,
		time.Now().UTC().Format(time.RFC3339), summary(out), runID)
	return out, nil
}

// GroupResults returns results grouped by identical (rendered, status).
func GroupResults(in []Result) []Group {
	by := map[string]*Group{}
	for _, r := range in {
		key := r.Hash + "|" + r.Status
		g, ok := by[key]
		if !ok {
			g = &Group{Hash: r.Hash, Rendered: r.Rendered, Status: r.Status}
			by[key] = g
		}
		g.Devices = append(g.Devices, r.Hostname)
	}
	out := make([]Group, 0, len(by))
	for _, g := range by {
		out = append(out, *g)
	}
	return out
}

func (s *Service) targets(ctx context.Context, tenantID int64, j Job) ([]devices.Device, error) {
	all, err := s.Devices.ListDevices(ctx, tenantID)
	if err != nil {
		return nil, err
	}
	if j.TargetGroupID == nil {
		return all, nil
	}
	var filtered []devices.Device
	for _, d := range all {
		if d.GroupID != nil && *d.GroupID == *j.TargetGroupID {
			filtered = append(filtered, d)
		}
	}
	return filtered, nil
}

func hashOf(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:8])
}

func summary(in []Result) string {
	for _, r := range in {
		if r.Status == "failed" || r.Status == "render_error" {
			return "partial"
		}
	}
	return "success"
}

func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func (s *Service) recordHighPriorityRollbackScaffold(ctx context.Context, tenantID int64, d devices.Device, applyErrMsg string) {
	if s.Audit == nil {
		return
	}
	detail := fmt.Sprintf("priority=high rollback=scaffold status=manual_required error=%s", sanitizeAuditError(applyErrMsg))
	s.Audit.Record(ctx, tenantID, 0, audit.SourceSystem, "automation.apply.rollback_scaffold", fmt.Sprintf("device:%d", d.ID), detail)
}

func sanitizeAuditError(msg string) string {
	msg = strings.ReplaceAll(msg, "\n", " ")
	msg = strings.ReplaceAll(msg, "\r", " ")
	const maxLen = 160
	if len(msg) > maxLen {
		msg = msg[:maxLen] + "..."
	}
	return msg
}

// Compile-time check that we use drivers somewhere (executor signature).
var _ = drivers.List
