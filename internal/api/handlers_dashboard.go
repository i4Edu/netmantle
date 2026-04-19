package api

import (
	"context"
	"database/sql"
	"net/http"
	"sort"
	"strconv"
	"time"

	"github.com/i4Edu/netmantle/internal/audit"
	"github.com/i4Edu/netmantle/internal/changereq"
	"github.com/i4Edu/netmantle/internal/compliance"
	"github.com/i4Edu/netmantle/internal/devices"
)

// dashboardSummary is the response body for GET /api/v1/dashboard/summary.
//
// All counts and per-driver tallies are scoped to the caller's tenant.
// Sparkline series are returned as 14 daily buckets, oldest first; values
// are 0..100 (percent) or absolute counts.
type dashboardSummary struct {
	Devices struct {
		Total       int `json:"total"`
		AddedRecent int `json:"added_recent"` // added in the last 7 days
	} `json:"devices"`

	Compliance struct {
		Percent      float64   `json:"percent"` // current pass / (pass+fail) * 100
		PassCount    int       `json:"pass_count"`
		FailCount    int       `json:"fail_count"`
		Sparkline14d []float64 `json:"sparkline_14d"` // historical % per day
	} `json:"compliance"`

	Backups struct {
		SuccessRate24h float64   `json:"success_rate_24h"` // %
		Total24h       int       `json:"total_24h"`
		Sparkline14d   []float64 `json:"sparkline_14d"` // % per day
	} `json:"backups"`

	Approvals struct {
		Pending   int    `json:"pending"`
		OldestAge string `json:"oldest_age,omitempty"` // human, e.g. "2h"
	} `json:"approvals"`

	StatusByDriver []driverStatus `json:"status_by_driver"`
	DriftHotspots  []driftHotspot `json:"drift_hotspots"`
	RecentEvents   []audit.Entry  `json:"recent_events"`

	Health struct {
		PollersTotal   int    `json:"pollers_total"`
		PollersHealthy int    `json:"pollers_healthy"`
		GitMirror      string `json:"git_mirror"` // "configured" | "not_configured" | "unknown"
	} `json:"health"`
}

type driverStatus struct {
	Driver    string `json:"driver"`
	Total     int    `json:"total"`
	Compliant int    `json:"compliant"` // devices with no failing finding
	Percent   int    `json:"percent"`
}

type driftHotspot struct {
	DeviceID  int64  `json:"device_id"`
	Hostname  string `json:"hostname"`
	Failing   int    `json:"failing"`
	TopDetail string `json:"top_detail,omitempty"`
}

// handleDashboardSummary aggregates a small, pre-computed view used by the
// landing page. It deliberately performs all aggregation server-side so the
// client only renders SVG and HTML — no per-row math, no extra round-trips.
//
// Non-fatal: any individual sub-query that fails returns a zero value for
// that section and the rest of the payload is still served. The dashboard
// must never break because, e.g., compliance has not been seeded yet.
func (s *server) handleDashboardSummary(w http.ResponseWriter, r *http.Request) {
	u := userFromContext(r.Context())
	ctx := r.Context()
	out := dashboardSummary{}
	now := time.Now().UTC()

	// Always return stable-shape 14-element sparkline arrays so the JSON
	// contract holds even when the DB type assertion below fails or the
	// dependency is unwired in tests.
	out.Compliance.Sparkline14d = make([]float64, 14)
	out.Backups.Sparkline14d = make([]float64, 14)

	// Fetch devices and findings once; reuse across all sections.
	var devs []devices.Device
	if s.Devices != nil {
		if d, err := s.Devices.ListDevices(ctx, u.TenantID); err == nil {
			devs = d
		}
	}
	var findings []compliance.Finding
	if s.Compliance != nil {
		if f, err := s.Compliance.ListFindings(ctx, u.TenantID); err == nil {
			findings = f
		}
	}

	// Devices total + recent + status-by-driver tallies.
	if devs != nil {
		out.Devices.Total = len(devs)
		cutoff := now.AddDate(0, 0, -7)
		byDriver := map[string]*driverStatus{}
		for _, d := range devs {
			if d.CreatedAt.After(cutoff) {
				out.Devices.AddedRecent++
			}
			if byDriver[d.Driver] == nil {
				byDriver[d.Driver] = &driverStatus{Driver: d.Driver}
			}
			byDriver[d.Driver].Total++
		}
		// Mark devices as compliant unless they have a failing finding.
		fails := map[int64]int{}
		for _, f := range findings {
			if f.Status == "fail" {
				fails[f.DeviceID]++
			}
		}
		for _, d := range devs {
			if fails[d.ID] == 0 {
				byDriver[d.Driver].Compliant++
			}
		}
		for _, ds := range byDriver {
			if ds.Total > 0 {
				ds.Percent = (ds.Compliant * 100) / ds.Total
			}
			out.StatusByDriver = append(out.StatusByDriver, *ds)
		}
		sort.Slice(out.StatusByDriver, func(i, j int) bool {
			return out.StatusByDriver[i].Total > out.StatusByDriver[j].Total
		})
	}

	// Compliance current % + drift hotspots (reuse `findings` and `devs`).
	if findings != nil {
		perDevice := map[int64]int{}
		for _, f := range findings {
			switch f.Status {
			case "pass":
				out.Compliance.PassCount++
			case "fail":
				out.Compliance.FailCount++
				perDevice[f.DeviceID]++
			}
		}
		total := out.Compliance.PassCount + out.Compliance.FailCount
		if total > 0 {
			out.Compliance.Percent = (float64(out.Compliance.PassCount) * 100.0) / float64(total)
		}
		// Drift hotspots (top 5 by failing-rule count).
		type kv struct {
			id    int64
			count int
		}
		arr := make([]kv, 0, len(perDevice))
		for id, c := range perDevice {
			arr = append(arr, kv{id, c})
		}
		sort.Slice(arr, func(i, j int) bool { return arr[i].count > arr[j].count })
		if len(arr) > 5 {
			arr = arr[:5]
		}
		// Reuse the device list already fetched at the top of the handler.
		hostByID := map[int64]string{}
		for _, d := range devs {
			hostByID[d.ID] = d.Hostname
		}
		// Build "top detail" string from the first failing finding per device.
		detailByID := map[int64]string{}
		for _, f := range findings {
			if f.Status != "fail" {
				continue
			}
			if _, ok := detailByID[f.DeviceID]; ok {
				continue
			}
			if f.Detail != "" {
				detailByID[f.DeviceID] = f.Detail
			}
		}
		for _, e := range arr {
			out.DriftHotspots = append(out.DriftHotspots, driftHotspot{
				DeviceID:  e.id,
				Hostname:  hostByID[e.id],
				Failing:   e.count,
				TopDetail: detailByID[e.id],
			})
		}
	}

	// Sparklines pulled from raw tables in a single sweep each.
	if db, ok := s.DB.(*sql.DB); ok && db != nil {
		out.Compliance.Sparkline14d = sparkBackupSuccess(ctx, db, u.TenantID, now, true)
		out.Backups.Sparkline14d = sparkBackupSuccess(ctx, db, u.TenantID, now, false)
		// 24h backup totals: derived as the last bucket of the success-rate query.
		var total24h, ok24h int
		_ = db.QueryRowContext(ctx, `
            SELECT COUNT(*),
                   SUM(CASE WHEN status='success' THEN 1 ELSE 0 END)
            FROM backup_runs br
            JOIN devices d ON d.id = br.device_id
            WHERE d.tenant_id = ?
              AND br.started_at >= ?`,
			u.TenantID, now.Add(-24*time.Hour).Format(time.RFC3339)).Scan(&total24h, &ok24h)
		out.Backups.Total24h = total24h
		if total24h > 0 {
			out.Backups.SuccessRate24h = (float64(ok24h) * 100.0) / float64(total24h)
		}
	}

	// Approvals pending.
	if s.ChangeReq != nil {
		crs, err := s.ChangeReq.List(ctx, u.TenantID, changereq.StatusSubmitted, 200)
		if err == nil {
			out.Approvals.Pending = len(crs)
			if len(crs) > 0 {
				oldest := crs[0].CreatedAt
				for _, c := range crs {
					if c.CreatedAt.Before(oldest) {
						oldest = c.CreatedAt
					}
				}
				out.Approvals.OldestAge = humanAge(now.Sub(oldest))
			}
		}
	}

	// Recent events: pull from audit.
	if s.Audit != nil {
		entries, err := s.Audit.List(ctx, audit.ListFilter{
			TenantID: u.TenantID, Limit: 20,
		})
		if err == nil {
			out.RecentEvents = entries
		}
	}
	if out.RecentEvents == nil {
		out.RecentEvents = []audit.Entry{}
	}

	// Health.
	if s.Pollers != nil {
		ps, err := s.Pollers.List(ctx, u.TenantID)
		if err == nil {
			out.Health.PollersTotal = len(ps)
			cutoff := now.Add(-2 * time.Minute)
			for _, p := range ps {
				if !p.LastSeen.IsZero() && p.LastSeen.After(cutoff) {
					out.Health.PollersHealthy++
				}
			}
		}
	}
	if s.GitOps != nil {
		m, err := s.GitOps.Get(ctx, u.TenantID)
		switch {
		case err != nil:
			out.Health.GitMirror = "unknown"
		case m == nil:
			out.Health.GitMirror = "not_configured"
		default:
			out.Health.GitMirror = "configured"
		}
	} else {
		out.Health.GitMirror = "not_configured"
	}

	if out.StatusByDriver == nil {
		out.StatusByDriver = []driverStatus{}
	}
	if out.DriftHotspots == nil {
		out.DriftHotspots = []driftHotspot{}
	}

	writeJSON(w, http.StatusOK, out)
}

// sparkBackupSuccess returns 14 daily buckets ending with `now`, oldest first.
//
// When `complianceMode` is true the value is the current overall compliance %
// repeated across all 14 buckets — a flat baseline. Future work would store
// per-day compliance snapshots so the trend line is meaningful; for now the
// flat line still shows the user a stable reference.
//
// When false it is the per-day backup success rate (success/total).
// Days with no data return 0 — the UI renders that as a flat baseline.
func sparkBackupSuccess(ctx context.Context, db *sql.DB, tenantID int64, now time.Time, complianceMode bool) []float64 {
	out := make([]float64, 14)
	end := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC).AddDate(0, 0, 1)
	start := end.AddDate(0, 0, -14)

	if complianceMode {
		var passNow, failNow int
		_ = db.QueryRowContext(ctx, `
            SELECT
              IFNULL(SUM(CASE WHEN status='pass' THEN 1 ELSE 0 END), 0),
              IFNULL(SUM(CASE WHEN status='fail' THEN 1 ELSE 0 END), 0)
            FROM compliance_findings cf
            JOIN devices d ON d.id = cf.device_id
            WHERE d.tenant_id = ?`, tenantID).Scan(&passNow, &failNow)
		if passNow+failNow == 0 {
			return out
		}
		v := (float64(passNow) * 100.0) / float64(passNow+failNow)
		for i := range out {
			out[i] = v
		}
		return out
	}

	rows, err := db.QueryContext(ctx, `
        SELECT substr(br.started_at, 1, 10) AS day,
               COUNT(*) AS total,
               SUM(CASE WHEN br.status='success' THEN 1 ELSE 0 END) AS ok
        FROM backup_runs br
        JOIN devices d ON d.id = br.device_id
        WHERE d.tenant_id = ?
          AND br.started_at >= ?
          AND br.started_at <  ?
        GROUP BY day`,
		tenantID, start.Format(time.RFC3339), end.Format(time.RFC3339))
	if err != nil {
		return out
	}
	defer rows.Close()
	byDay := map[string]float64{}
	for rows.Next() {
		var day string
		var total, ok int
		if err := rows.Scan(&day, &total, &ok); err != nil {
			continue
		}
		if total > 0 {
			byDay[day] = (float64(ok) * 100.0) / float64(total)
		}
	}
	for i := 0; i < 14; i++ {
		d := start.AddDate(0, 0, i).Format("2006-01-02")
		out[i] = byDay[d]
	}
	return out
}

func humanAge(d time.Duration) string {
	switch {
	case d < time.Minute:
		return "<1m"
	case d < time.Hour:
		return strconv.Itoa(int(d/time.Minute)) + "m"
	case d < 24*time.Hour:
		return strconv.Itoa(int(d/time.Hour)) + "h"
	default:
		return strconv.Itoa(int(d/(24*time.Hour))) + "d"
	}
}
