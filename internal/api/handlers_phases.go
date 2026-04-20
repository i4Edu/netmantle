package api

import (
	"archive/zip"
	"database/sql"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/i4Edu/netmantle/internal/automation"
	"github.com/i4Edu/netmantle/internal/changes"
	"github.com/i4Edu/netmantle/internal/compliance"
	"github.com/i4Edu/netmantle/internal/compliance/rulepacks"
	"github.com/i4Edu/netmantle/internal/configstore"
	"github.com/i4Edu/netmantle/internal/devices"
	"github.com/i4Edu/netmantle/internal/discovery"
	"github.com/i4Edu/netmantle/internal/netops"
	"github.com/i4Edu/netmantle/internal/notify"
	"github.com/i4Edu/netmantle/internal/poller"
	"github.com/i4Edu/netmantle/internal/probes"
	"github.com/i4Edu/netmantle/internal/search"
)

// ============================================================
// Phase 2 — changes & notifications
// ============================================================

func (s *server) handleListChanges(w http.ResponseWriter, r *http.Request) {
	u := userFromContext(r.Context())
	var deviceID int64
	if v := r.URL.Query().Get("device_id"); v != "" {
		deviceID, _ = strconv.ParseInt(v, 10, 64)
	}
	out, err := s.Changes.ListByDevice(r.Context(), u.TenantID, deviceID, 100)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if out == nil {
		out = []changes.Event{}
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *server) handleChangeDiff(w http.ResponseWriter, r *http.Request) {
	u := userFromContext(r.Context())
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	d, err := s.Changes.Diff(r.Context(), u.TenantID, id)
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = w.Write([]byte(d))
}

func (s *server) handleMarkReviewed(w http.ResponseWriter, r *http.Request) {
	u := userFromContext(r.Context())
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	if err := s.Changes.MarkReviewed(r.Context(), u.TenantID, id); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *server) handleListChannels(w http.ResponseWriter, r *http.Request) {
	u := userFromContext(r.Context())
	out, err := s.Notify.ListChannels(r.Context(), u.TenantID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if out == nil {
		out = []notify.Channel{}
	}
	writeJSON(w, http.StatusOK, out)
}

type createChannelInput struct {
	Name   string          `json:"name"`
	Kind   string          `json:"kind"`
	Config json.RawMessage `json:"config"`
}

func (s *server) handleCreateChannel(w http.ResponseWriter, r *http.Request) {
	u := userFromContext(r.Context())
	var in createChannelInput
	if err := decodeJSON(r, &in); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	c, err := s.Notify.CreateChannel(r.Context(), u.TenantID, in.Name, in.Kind, in.Config)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, c)
}

func (s *server) handleDeleteChannel(w http.ResponseWriter, r *http.Request) {
	u := userFromContext(r.Context())
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	if err := s.Notify.DeleteChannel(r.Context(), u.TenantID, id); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *server) handleListRules(w http.ResponseWriter, r *http.Request) {
	u := userFromContext(r.Context())
	out, err := s.Notify.ListRules(r.Context(), u.TenantID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if out == nil {
		out = []map[string]any{}
	}
	writeJSON(w, http.StatusOK, out)
}

type createRuleInput struct {
	Name      string `json:"name"`
	EventType string `json:"event_type"`
	ChannelID int64  `json:"channel_id"`
}

func (s *server) handleCreateRule(w http.ResponseWriter, r *http.Request) {
	u := userFromContext(r.Context())
	var in createRuleInput
	if err := decodeJSON(r, &in); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := s.Notify.CreateRule(r.Context(), u.TenantID, in.Name, in.EventType, in.ChannelID); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	w.WriteHeader(http.StatusCreated)
}

// ============================================================
// Phase 3 — search & export
// ============================================================

func (s *server) handleSearch(w http.ResponseWriter, r *http.Request) {
	u := userFromContext(r.Context())
	q := r.URL.Query().Get("q")
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	hits, err := s.Search.Query(r.Context(), u.TenantID, q, limit)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if hits == nil {
		hits = []search.Hit{}
	}
	writeJSON(w, http.StatusOK, hits)
}

func (s *server) handleListSaved(w http.ResponseWriter, r *http.Request) {
	u := userFromContext(r.Context())
	out, err := s.Search.ListSaved(r.Context(), u.TenantID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if out == nil {
		out = []search.SavedSearch{}
	}
	writeJSON(w, http.StatusOK, out)
}

type saveSearchInput struct {
	Name            string `json:"name"`
	Query           string `json:"query"`
	NotifyChannelID *int64 `json:"notify_channel_id,omitempty"`
}

func (s *server) handleSaveSearch(w http.ResponseWriter, r *http.Request) {
	u := userFromContext(r.Context())
	var in saveSearchInput
	if err := decodeJSON(r, &in); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	id, err := s.Search.SaveSearch(r.Context(), u.TenantID, u.ID, in.Name, in.Query, in.NotifyChannelID)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, map[string]int64{"id": id})
}

func (s *server) handleChangesCSV(w http.ResponseWriter, r *http.Request) {
	u := userFromContext(r.Context())
	out, err := s.Changes.List(r.Context(), u.TenantID, 1000)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", "attachment; filename=changes.csv")
	cw := csv.NewWriter(w)
	defer cw.Flush()
	_ = cw.Write([]string{"id", "device_id", "artifact", "old_sha", "new_sha", "added", "removed", "reviewed", "created_at"})
	for _, e := range out {
		_ = cw.Write([]string{
			strconv.FormatInt(e.ID, 10),
			strconv.FormatInt(e.DeviceID, 10),
			e.Artifact, e.OldSHA, e.NewSHA,
			strconv.Itoa(e.Added), strconv.Itoa(e.Removed),
			strconv.FormatBool(e.Reviewed),
			e.CreatedAt.Format(time.RFC3339),
		})
	}
}

// ============================================================
// Phase 4 — compliance
// ============================================================

func (s *server) handleListComplianceRules(w http.ResponseWriter, r *http.Request) {
	u := userFromContext(r.Context())
	out, err := s.Compliance.ListRules(r.Context(), u.TenantID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if out == nil {
		out = []compliance.Rule{}
	}
	writeJSON(w, http.StatusOK, out)
}

type complianceRuleInput struct {
	Name        string `json:"name"`
	Kind        string `json:"kind"`
	Pattern     string `json:"pattern"`
	Severity    string `json:"severity,omitempty"`
	Description string `json:"description,omitempty"`
}

func (s *server) handleCreateComplianceRule(w http.ResponseWriter, r *http.Request) {
	u := userFromContext(r.Context())
	var in complianceRuleInput
	if err := decodeJSON(r, &in); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	rule, err := s.Compliance.CreateRule(r.Context(), compliance.Rule{
		TenantID: u.TenantID, Name: in.Name, Kind: in.Kind, Pattern: in.Pattern,
		Severity: in.Severity, Description: in.Description,
	})
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, rule)
}

func (s *server) handleDeleteComplianceRule(w http.ResponseWriter, r *http.Request) {
	u := userFromContext(r.Context())
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	if err := s.Compliance.DeleteRule(r.Context(), u.TenantID, id); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *server) handleListFindings(w http.ResponseWriter, r *http.Request) {
	u := userFromContext(r.Context())
	out, err := s.Compliance.ListFindings(r.Context(), u.TenantID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if out == nil {
		out = []compliance.Finding{}
	}
	writeJSON(w, http.StatusOK, out)
}

// handleListRulePacks returns the catalogue of built-in compliance rule packs.
func (s *server) handleListRulePacks(w http.ResponseWriter, _ *http.Request) {
	all := rulepacks.All()
	type packMeta struct {
		Name        string `json:"name"`
		Version     string `json:"version"`
		Description string `json:"description"`
		RuleCount   int    `json:"rule_count"`
	}
	out := make([]packMeta, 0, len(all))
	for _, p := range all {
		out = append(out, packMeta{
			Name: p.Name, Version: p.Version,
			Description: p.Description, RuleCount: len(p.Rules),
		})
	}
	writeJSON(w, http.StatusOK, out)
}

// handleApplyRulePack applies a named rule pack to the caller's tenant.
// Rules are upserted by name, so the call is idempotent and safe to
// re-apply after a pack version bump. Requires operator or admin role.
func (s *server) handleApplyRulePack(w http.ResponseWriter, r *http.Request) {
	u := userFromContext(r.Context())
	packName := r.PathValue("name")
	pack, ok := rulepacks.Get(packName)
	if !ok {
		writeError(w, http.StatusNotFound, "compliance: rule pack not found: "+packName)
		return
	}

	var applied []compliance.Rule
	for _, tmpl := range pack.Rules {
		tmpl.TenantID = u.TenantID
		rule, err := s.Compliance.UpsertRule(r.Context(), tmpl)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		applied = append(applied, rule)
	}
	if applied == nil {
		applied = []compliance.Rule{}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"pack":    pack.Name,
		"version": pack.Version,
		"applied": len(applied),
		"rules":   applied,
	})
}

type groupRulePackAssignment struct {
	GroupID   int64    `json:"group_id"`
	GroupName string   `json:"group_name,omitempty"`
	Packs     []string `json:"packs"`
}

type setGroupRulePackAssignmentInput struct {
	Packs []string `json:"packs"`
}

func (s *server) handleListGroupRulePackAssignments(w http.ResponseWriter, r *http.Request) {
	u := userFromContext(r.Context())
	db, ok := s.DB.(*sql.DB)
	if !ok || db == nil {
		writeError(w, http.StatusInternalServerError, "compliance: database unavailable")
		return
	}
	rows, err := db.QueryContext(r.Context(), `
        SELECT g.id, g.name, a.pack_name
        FROM device_groups g
        LEFT JOIN compliance_rulepack_assignments a
            ON a.tenant_id=g.tenant_id AND a.group_id=g.id
        WHERE g.tenant_id=?
        ORDER BY g.name ASC, a.pack_name ASC`,
		u.TenantID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	defer rows.Close()
	type bucket struct {
		name  string
		packs []string
	}
	byGroup := map[int64]*bucket{}
	order := []int64{}
	for rows.Next() {
		var (
			groupID int64
			name    string
			pack    sql.NullString
		)
		if err := rows.Scan(&groupID, &name, &pack); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		b, exists := byGroup[groupID]
		if !exists {
			b = &bucket{name: name}
			byGroup[groupID] = b
			order = append(order, groupID)
		}
		if pack.Valid && pack.String != "" {
			b.packs = append(b.packs, pack.String)
		}
	}
	out := make([]groupRulePackAssignment, 0, len(order))
	for _, gid := range order {
		b := byGroup[gid]
		if b.packs == nil {
			b.packs = []string{}
		}
		out = append(out, groupRulePackAssignment{
			GroupID: gid, GroupName: b.name, Packs: b.packs,
		})
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *server) handleSetGroupRulePackAssignments(w http.ResponseWriter, r *http.Request) {
	u := userFromContext(r.Context())
	groupID, ok := pathID(w, r)
	if !ok {
		return
	}
	db, ok := s.DB.(*sql.DB)
	if !ok || db == nil {
		writeError(w, http.StatusInternalServerError, "compliance: database unavailable")
		return
	}
	var in setGroupRulePackAssignmentInput
	if err := decodeJSON(r, &in); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	// Ensure the group exists in this tenant.
	var exists int
	if err := db.QueryRowContext(r.Context(),
		`SELECT COUNT(1) FROM device_groups WHERE tenant_id=? AND id=?`,
		u.TenantID, groupID).Scan(&exists); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if exists == 0 {
		writeError(w, http.StatusNotFound, "device group not found")
		return
	}
	// Validate all requested pack names and normalize duplicates.
	seenPacks := make(map[string]struct{}, len(in.Packs))
	uniquePacks := make([]string, 0, len(in.Packs))
	for _, p := range in.Packs {
		if _, found := rulepacks.Get(p); !found {
			writeError(w, http.StatusBadRequest, "unknown rule pack: "+p)
			return
		}
		if _, seen := seenPacks[p]; seen {
			continue
		}
		seenPacks[p] = struct{}{}
		uniquePacks = append(uniquePacks, p)
	}
	in.Packs = uniquePacks

	tx, err := db.BeginTx(r.Context(), nil)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(r.Context(),
		`DELETE FROM compliance_rulepack_assignments WHERE tenant_id=? AND group_id=?`,
		u.TenantID, groupID); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	// Rebuild scoped rules for this group from selected packs.
	if _, err := tx.ExecContext(r.Context(),
		`DELETE FROM compliance_rules WHERE tenant_id=? AND group_id=?`,
		u.TenantID, groupID); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	now := time.Now().UTC().Format(time.RFC3339)
	for _, packName := range in.Packs {
		pack, _ := rulepacks.Get(packName)
		if _, err := tx.ExecContext(r.Context(), `
            INSERT INTO compliance_rulepack_assignments(tenant_id, group_id, pack_name, created_at)
            VALUES(?, ?, ?, ?)`,
			u.TenantID, groupID, packName, now); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		for _, tmpl := range pack.Rules {
			if _, err := tx.ExecContext(r.Context(), `
                INSERT INTO compliance_rules(tenant_id, group_id, name, kind, pattern, severity, description, created_at)
                VALUES(?, ?, ?, ?, ?, ?, ?, ?)`,
				u.TenantID, groupID, tmpl.Name, tmpl.Kind, tmpl.Pattern, tmpl.Severity, tmpl.Description, now); err != nil {
				writeError(w, http.StatusInternalServerError, err.Error())
				return
			}
		}
	}
	if err := tx.Commit(); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"group_id": groupID,
		"packs":    in.Packs,
		"applied":  len(in.Packs),
	})
}

// ============================================================
// Phase 5 — discovery
// ============================================================

type startScanInput struct {
	CIDR string `json:"cidr"`
	Port int    `json:"port,omitempty"`
}

func (s *server) handleStartScan(w http.ResponseWriter, r *http.Request) {
	u := userFromContext(r.Context())
	var in startScanInput
	if err := decodeJSON(r, &in); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	scan, results, err := s.Discovery.Run(r.Context(), u.TenantID, in.CIDR, in.Port, 32)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if results == nil {
		results = []discovery.Result{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"scan": scan, "results": results})
}

func (s *server) handleImportNetBox(w http.ResponseWriter, r *http.Request) {
	u := userFromContext(r.Context())
	body, err := readAllLimited(r, 5<<20)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	items, err := discovery.ImportNetBox(body)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"tenant_id": u.TenantID, "candidates": items,
	})
}

// ============================================================
// Phase 6 — push automation
// ============================================================

func (s *server) handleListPushJobs(w http.ResponseWriter, r *http.Request) {
	u := userFromContext(r.Context())
	out, err := s.Automation.ListJobs(r.Context(), u.TenantID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if out == nil {
		out = []automation.Job{}
	}
	writeJSON(w, http.StatusOK, out)
}

type createPushJobInput struct {
	Name             string            `json:"name"`
	Template         string            `json:"template"`
	Variables        map[string]string `json:"variables,omitempty"`
	TargetGroupID    *int64            `json:"target_group_id,omitempty"`
	VerifyCommand    string            `json:"verify_command,omitempty"`
	RollbackTemplate string            `json:"rollback_template,omitempty"`
}

func (s *server) handleCreatePushJob(w http.ResponseWriter, r *http.Request) {
	u := userFromContext(r.Context())
	var in createPushJobInput
	if err := decodeJSON(r, &in); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	j, err := s.Automation.CreateJob(r.Context(), automation.Job{
		TenantID: u.TenantID, Name: in.Name, Template: in.Template,
		Variables: in.Variables, TargetGroupID: in.TargetGroupID,
	})
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	// Persist verify_command and rollback_template if provided.
	if in.VerifyCommand != "" || in.RollbackTemplate != "" {
		if db, ok := s.DB.(*sql.DB); ok && db != nil {
			_, _ = db.ExecContext(r.Context(),
				`UPDATE push_jobs SET verify_command=?, rollback_template=? WHERE id=?`,
				in.VerifyCommand, in.RollbackTemplate, j.ID)
		}
	}
	writeJSON(w, http.StatusCreated, j)
}

func (s *server) handlePreviewPush(w http.ResponseWriter, r *http.Request) {
	u := userFromContext(r.Context())
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	results, err := s.Automation.Preview(r.Context(), u.TenantID, id)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"results": results, "groups": automation.GroupResults(results),
	})
}

func (s *server) handleRunPush(w http.ResponseWriter, r *http.Request) {
	u := userFromContext(r.Context())
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	results, err := s.Automation.Run(r.Context(), u.TenantID, id, 4)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"results": results, "groups": automation.GroupResults(results),
	})
}

// ============================================================
// Phase 7 — pollers + terminal
// ============================================================

func (s *server) handleListPollers(w http.ResponseWriter, r *http.Request) {
	u := userFromContext(r.Context())
	out, err := s.Pollers.List(r.Context(), u.TenantID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if out == nil {
		out = []poller.Poller{}
	}
	writeJSON(w, http.StatusOK, out)
}

type registerPollerInput struct {
	Zone string `json:"zone"`
	Name string `json:"name"`
}

func (s *server) handleRegisterPoller(w http.ResponseWriter, r *http.Request) {
	u := userFromContext(r.Context())
	var in registerPollerInput
	if err := decodeJSON(r, &in); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	p, token, err := s.Pollers.Register(r.Context(), u.TenantID, in.Zone, in.Name)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"poller":          p,
		"bootstrap_token": token,
		"warning":         "store this token now — it will not be shown again",
	})
}

func (s *server) handleDeletePoller(w http.ResponseWriter, r *http.Request) {
	u := userFromContext(r.Context())
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	if err := s.Pollers.Delete(r.Context(), u.TenantID, id); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *server) handleTerminal() http.Handler {
	return s.Terminal.Handler(func(r *http.Request) (int64, int64, int64, bool) {
		u := userFromContext(r.Context())
		if u == nil {
			return 0, 0, 0, false
		}
		raw := r.PathValue("id")
		id, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || id <= 0 {
			return 0, 0, 0, false
		}
		// Confirm device exists for this tenant.
		if _, err := s.Devices.GetDevice(r.Context(), u.TenantID, id); err != nil {
			return 0, 0, 0, false
		}
		return u.TenantID, u.ID, id, true
	})
}

// ============================================================
// Phase 8 — probes
// ============================================================

func (s *server) handleListProbes(w http.ResponseWriter, r *http.Request) {
	u := userFromContext(r.Context())
	out, err := s.Probes.List(r.Context(), u.TenantID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if out == nil {
		out = []probes.Probe{}
	}
	writeJSON(w, http.StatusOK, out)
}

type probeInput struct {
	Name      string `json:"name"`
	Command   string `json:"command"`
	IntervalS int    `json:"interval_s,omitempty"`
}

func (s *server) handleCreateProbe(w http.ResponseWriter, r *http.Request) {
	u := userFromContext(r.Context())
	var in probeInput
	if err := decodeJSON(r, &in); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	p, err := s.Probes.Create(r.Context(), probes.Probe{
		TenantID: u.TenantID, Name: in.Name, Command: in.Command, IntervalS: in.IntervalS,
	})
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, p)
}

func (s *server) handleDeleteProbe(w http.ResponseWriter, r *http.Request) {
	u := userFromContext(r.Context())
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	if err := s.Probes.Delete(r.Context(), u.TenantID, id); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleRunProbeNow runs a probe against all tenant devices immediately.
// It uses the backup service's session factory so credential resolution
// matches the regular backup path.
func (s *server) handleRunProbeNow(w http.ResponseWriter, r *http.Request) {
	u := userFromContext(r.Context())
	probeID, ok := pathID(w, r)
	if !ok {
		return
	}

	// Load the probe to get its command.
	ps, err := s.Probes.List(r.Context(), u.TenantID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	var probe *probes.Probe
	for i := range ps {
		if ps[i].ID == probeID {
			probe = &ps[i]
			break
		}
	}
	if probe == nil {
		writeError(w, http.StatusNotFound, "probe not found")
		return
	}

	// Load devices.
	devs, err := s.Devices.ListDevices(r.Context(), u.TenantID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	// Run the probe concurrently against each device (max 10 parallel).
	sem := make(chan struct{}, 10)
	type result struct {
		DeviceID int64  `json:"device_id"`
		Hostname string `json:"hostname"`
		Status   string `json:"status"`
		Error    string `json:"error,omitempty"`
	}
	results := make([]result, len(devs))
	var wg sync.WaitGroup
	for i, dev := range devs {
		if dev.CredentialID == nil {
			results[i] = result{DeviceID: dev.ID, Hostname: dev.Hostname, Status: "skipped", Error: "no credential"}
			continue
		}
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			out, runErr := s.Backup.RunProbe(r.Context(), devs[i], probe.Command)
			res := result{DeviceID: devs[i].ID, Hostname: devs[i].Hostname, Status: "ok"}
			if runErr != nil {
				res.Status = "failed"
				res.Error = runErr.Error()
				_ = s.Probes.RecordRun(r.Context(), probeID, devs[i].ID, "", runErr)
			} else {
				_ = s.Probes.RecordRun(r.Context(), probeID, devs[i].ID, out, nil)
			}
			results[i] = res
		}(i)
	}
	wg.Wait()
	writeJSON(w, http.StatusOK, map[string]any{"probe_id": probeID, "results": results})
}

// handleTopologyDiscover ensures a "neighbors" probe exists for the tenant
// and runs it against all devices immediately. This seeds the topology graph.
func (s *server) handleTopologyDiscover(w http.ResponseWriter, r *http.Request) {
	u := userFromContext(r.Context())
	ctx := r.Context()

	// Find or create the "neighbors" probe.
	ps, err := s.Probes.List(ctx, u.TenantID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	var neighborProbe *probes.Probe
	for i := range ps {
		if ps[i].Name == "neighbors" {
			neighborProbe = &ps[i]
			break
		}
	}
	if neighborProbe == nil {
		// Create with a sensible default command covering LLDP and CDP.
		// Drivers that don't support these commands will record an error run.
		p, err := s.Probes.Create(ctx, probes.Probe{
			TenantID:  u.TenantID,
			Name:      "neighbors",
			Command:   "show lldp neighbors",
			IntervalS: 3600,
		})
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		neighborProbe = &p
	}

	// Run against all devices.
	devs, err := s.Devices.ListDevices(ctx, u.TenantID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	sem := make(chan struct{}, 8)
	type devResult struct {
		DeviceID int64  `json:"device_id"`
		Hostname string `json:"hostname"`
		Status   string `json:"status"`
		Links    int    `json:"links"`
		Error    string `json:"error,omitempty"`
	}
	results := make([]devResult, len(devs))
	var wg sync.WaitGroup
	for i, dev := range devs {
		if dev.CredentialID == nil {
			results[i] = devResult{DeviceID: dev.ID, Hostname: dev.Hostname, Status: "skipped", Error: "no credential"}
			continue
		}
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			out, runErr := s.Backup.RunProbe(ctx, devs[i], neighborProbe.Command)
			res := devResult{DeviceID: devs[i].ID, Hostname: devs[i].Hostname, Status: "ok"}
			if runErr != nil {
				res.Status = "failed"
				res.Error = runErr.Error()
				_ = s.Probes.RecordRun(ctx, neighborProbe.ID, devs[i].ID, "", runErr)
			} else {
				_ = s.Probes.RecordRun(ctx, neighborProbe.ID, devs[i].ID, out, nil)
				res.Links = len(netops.FromNeighborOutput(devs[i].Hostname, out))
			}
			results[i] = res
		}(i)
	}
	wg.Wait()
	writeJSON(w, http.StatusOK, map[string]any{"probe_id": neighborProbe.ID, "results": results})
}

func (s *server) handleListProbeRuns(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	out, err := s.Probes.LatestRuns(r.Context(), id, limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if out == nil {
		out = []probes.Run{}
	}
	writeJSON(w, http.StatusOK, out)
}

// ============================================================
// Phase 9 — tenants & quotas
// ============================================================

func (s *server) handleListTenants(w http.ResponseWriter, r *http.Request) {
	out, err := s.Tenants.List(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, out)
}

type tenantInput struct {
	Name       string `json:"name"`
	MaxDevices int    `json:"max_devices,omitempty"`
}

func (s *server) handleCreateTenant(w http.ResponseWriter, r *http.Request) {
	var in tenantInput
	if err := decodeJSON(r, &in); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	t, err := s.Tenants.Create(r.Context(), in.Name, in.MaxDevices)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, t)
}

type quotaInput struct {
	MaxDevices int `json:"max_devices"`
}

func (s *server) handleSetTenantQuota(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var in quotaInput
	if err := decodeJSON(r, &in); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := s.Tenants.SetQuota(r.Context(), id, in.MaxDevices); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ============================================================
// Phase 10 — topology + GitOps
// ============================================================

func (s *server) handleTopology(w http.ResponseWriter, r *http.Request) {
	u := userFromContext(r.Context())
	// Topology is built from the latest probe-run output named "neighbors"
	// per device. If no such probe results exist, return an empty graph.
	db, ok := s.DB.(*sql.DB)
	if !ok {
		writeJSON(w, http.StatusOK, netops.Graph{APIVersion: "1.0", Links: []netops.Link{}})
		return
	}

	// Optional ?limit= cap on how many devices contribute to the graph.
	// Defaults to 500 to prevent unbounded queries on large tenants.
	limit := 500
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 10000 {
			limit = n
		}
	}

	// Select the latest probe run per device using a CTE so the LIMIT
	// applies to distinct devices, not to probe_runs rows. This prevents
	// a chatty device with many recent runs from crowding out other devices.
	rows, err := db.QueryContext(r.Context(), `
        WITH latest AS (
            SELECT pr.device_id, MAX(pr.id) AS max_id
            FROM probe_runs pr
            JOIN probes p ON p.id = pr.probe_id
            WHERE p.tenant_id = ? AND p.name = 'neighbors'
              AND pr.created_at >= ?
            GROUP BY pr.device_id
            LIMIT ?
        )
        SELECT d.hostname, pr.output
        FROM latest l
        JOIN probe_runs pr ON pr.id = l.max_id
        JOIN devices d ON d.id = pr.device_id`,
		u.TenantID,
		time.Now().UTC().Add(-7*24*time.Hour).Format(time.RFC3339),
		limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	defer rows.Close()
	per := map[string][]netops.Link{}
	for rows.Next() {
		var host, output string
		if err := rows.Scan(&host, &output); err != nil {
			continue
		}
		if _, seen := per[host]; seen {
			continue
		}
		per[host] = netops.FromNeighborOutput(host, output)
	}
	g := netops.Merge(per)
	if g.Links == nil {
		g.Links = []netops.Link{}
	}
	writeJSON(w, http.StatusOK, g)
}

func (s *server) handleGetMirror(w http.ResponseWriter, r *http.Request) {
	u := userFromContext(r.Context())
	m, err := s.GitOps.Get(r.Context(), u.TenantID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if m == nil {
		writeJSON(w, http.StatusOK, map[string]any{"configured": false})
		return
	}
	writeJSON(w, http.StatusOK, m)
}

type mirrorInput struct {
	RemoteURL string `json:"remote_url"`
	Branch    string `json:"branch,omitempty"`
	Token     string `json:"token,omitempty"`
}

func (s *server) handlePutMirror(w http.ResponseWriter, r *http.Request) {
	u := userFromContext(r.Context())
	var in mirrorInput
	if err := decodeJSON(r, &in); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := s.GitOps.Configure(r.Context(), u.TenantID, in.RemoteURL, in.Branch, in.Token); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ============================================================

// ============================================================
// Config version history + bulk export
// ============================================================

func (s *server) handleListConfigVersions(w http.ResponseWriter, r *http.Request) {
	u := userFromContext(r.Context())
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	if s.ConfigStore == nil {
		writeError(w, http.StatusInternalServerError, "config store not configured")
		return
	}
	limit := 50
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			limit = n
		}
	}
	entries, err := s.ConfigStore.ListVersions(u.TenantID, id, limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if entries == nil {
		entries = []configstore.VersionEntry{}
	}
	writeJSON(w, http.StatusOK, entries)
}

type exportConfigsInput struct {
	DeviceIDs []int64 `json:"device_ids"`
	Format    string  `json:"format"`
}

func (s *server) handleExportConfigs(w http.ResponseWriter, r *http.Request) {
	u := userFromContext(r.Context())
	var in exportConfigsInput
	if err := decodeJSON(r, &in); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if len(in.DeviceIDs) == 0 {
		writeError(w, http.StatusBadRequest, "device_ids required")
		return
	}

	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", "attachment; filename=configs.zip")
	zw := zip.NewWriter(w)
	defer zw.Close()

	for _, devID := range in.DeviceIDs {
		dev, err := s.Devices.GetDevice(r.Context(), u.TenantID, devID)
		if err != nil {
			continue
		}
		body, _, err := s.Backup.LatestVersion(r.Context(), u.TenantID, devID, "")
		if err != nil {
			continue
		}
		fw, err := zw.Create(dev.Hostname + ".cfg")
		if err != nil {
			continue
		}
		_, _ = fw.Write(body)
	}
}

// ============================================================
// Push job preflight
// ============================================================

type preflightResult struct {
	DeviceID  int64  `json:"device_id"`
	Hostname  string `json:"hostname"`
	Reachable bool   `json:"reachable"`
	LatencyMs int64  `json:"latency_ms,omitempty"`
	Error     string `json:"error,omitempty"`
}

func (s *server) handlePreflight(w http.ResponseWriter, r *http.Request) {
	u := userFromContext(r.Context())
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	j, err := s.Automation.GetJob(r.Context(), u.TenantID, id)
	if err != nil {
		writeError(w, http.StatusNotFound, "job not found")
		return
	}
	devs, err := s.Devices.ListDevices(r.Context(), u.TenantID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	// Filter to target group if set.
	var targets []devices.Device
	for _, d := range devs {
		if j.TargetGroupID == nil || (d.GroupID != nil && *d.GroupID == *j.TargetGroupID) {
			targets = append(targets, d)
		}
	}

	results := make([]preflightResult, len(targets))
	var wg sync.WaitGroup
	for i, d := range targets {
		wg.Add(1)
		go func(idx int, dev devices.Device) {
			defer wg.Done()
			pr := preflightResult{DeviceID: dev.ID, Hostname: dev.Hostname}
			addr := net.JoinHostPort(dev.Address, strconv.Itoa(dev.Port))
			start := time.Now()
			conn, dialErr := net.DialTimeout("tcp", addr, 5*time.Second)
			if dialErr != nil {
				pr.Error = dialErr.Error()
			} else {
				pr.Reachable = true
				pr.LatencyMs = time.Since(start).Milliseconds()
				conn.Close()
			}
			results[idx] = pr
		}(i, d)
	}
	wg.Wait()
	writeJSON(w, http.StatusOK, map[string]any{"results": results})
}

// ============================================================
// Schedules CRUD
// ============================================================

type Schedule struct {
	ID        int64  `json:"id"`
	TenantID  int64  `json:"tenant_id"`
	Kind      string `json:"kind"`
	Name      string `json:"name"`
	CronExpr  string `json:"cron_expr"`
	TargetID  int64  `json:"target_id"`
	Enabled   bool   `json:"enabled"`
	LastRunAt string `json:"last_run_at,omitempty"`
	NextRunAt string `json:"next_run_at,omitempty"`
	CreatedAt string `json:"created_at,omitempty"`
}

func (s *server) handleListSchedules(w http.ResponseWriter, r *http.Request) {
	u := userFromContext(r.Context())
	db, ok := s.DB.(*sql.DB)
	if !ok || db == nil {
		writeError(w, http.StatusInternalServerError, "database unavailable")
		return
	}
	rows, err := db.QueryContext(r.Context(),
		`SELECT id, tenant_id, kind, name, cron_expr, target_id, enabled, last_run_at, next_run_at, created_at
		 FROM schedules WHERE tenant_id=? ORDER BY id`, u.TenantID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	defer rows.Close()
	var out []Schedule
	for rows.Next() {
		var sc Schedule
		var enabled int
		if err := rows.Scan(&sc.ID, &sc.TenantID, &sc.Kind, &sc.Name, &sc.CronExpr,
			&sc.TargetID, &enabled, &sc.LastRunAt, &sc.NextRunAt, &sc.CreatedAt); err != nil {
			continue
		}
		sc.Enabled = enabled != 0
		out = append(out, sc)
	}
	if out == nil {
		out = []Schedule{}
	}
	writeJSON(w, http.StatusOK, out)
}

type createScheduleInput struct {
	Kind     string `json:"kind"`
	Name     string `json:"name"`
	CronExpr string `json:"cron_expr"`
	TargetID int64  `json:"target_id"`
	Enabled  *bool  `json:"enabled,omitempty"`
}

func (s *server) handleCreateSchedule(w http.ResponseWriter, r *http.Request) {
	u := userFromContext(r.Context())
	db, ok := s.DB.(*sql.DB)
	if !ok || db == nil {
		writeError(w, http.StatusInternalServerError, "database unavailable")
		return
	}
	var in createScheduleInput
	if err := decodeJSON(r, &in); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if in.Kind != "backup" && in.Kind != "push" {
		writeError(w, http.StatusBadRequest, "kind must be 'backup' or 'push'")
		return
	}
	enabled := 1
	if in.Enabled != nil && !*in.Enabled {
		enabled = 0
	}
	res, err := db.ExecContext(r.Context(),
		`INSERT INTO schedules(tenant_id, kind, name, cron_expr, target_id, enabled) VALUES(?, ?, ?, ?, ?, ?)`,
		u.TenantID, in.Kind, in.Name, in.CronExpr, in.TargetID, enabled)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	id, _ := res.LastInsertId()
	writeJSON(w, http.StatusCreated, Schedule{
		ID: id, TenantID: u.TenantID, Kind: in.Kind, Name: in.Name,
		CronExpr: in.CronExpr, TargetID: in.TargetID, Enabled: enabled != 0,
	})
}

type updateScheduleInput struct {
	Name     *string `json:"name,omitempty"`
	CronExpr *string `json:"cron_expr,omitempty"`
	TargetID *int64  `json:"target_id,omitempty"`
	Enabled  *bool   `json:"enabled,omitempty"`
}

func (s *server) handleUpdateSchedule(w http.ResponseWriter, r *http.Request) {
	u := userFromContext(r.Context())
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	db, dbOK := s.DB.(*sql.DB)
	if !dbOK || db == nil {
		writeError(w, http.StatusInternalServerError, "database unavailable")
		return
	}
	var in updateScheduleInput
	if err := decodeJSON(r, &in); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	// Verify schedule belongs to this tenant.
	var exists int
	if err := db.QueryRowContext(r.Context(),
		`SELECT COUNT(1) FROM schedules WHERE tenant_id=? AND id=?`, u.TenantID, id).Scan(&exists); err != nil || exists == 0 {
		writeError(w, http.StatusNotFound, "schedule not found")
		return
	}
	if in.Name != nil {
		db.ExecContext(r.Context(), `UPDATE schedules SET name=? WHERE tenant_id=? AND id=?`, *in.Name, u.TenantID, id)
	}
	if in.CronExpr != nil {
		db.ExecContext(r.Context(), `UPDATE schedules SET cron_expr=? WHERE tenant_id=? AND id=?`, *in.CronExpr, u.TenantID, id)
	}
	if in.TargetID != nil {
		db.ExecContext(r.Context(), `UPDATE schedules SET target_id=? WHERE tenant_id=? AND id=?`, *in.TargetID, u.TenantID, id)
	}
	if in.Enabled != nil {
		v := 0
		if *in.Enabled {
			v = 1
		}
		db.ExecContext(r.Context(), `UPDATE schedules SET enabled=? WHERE tenant_id=? AND id=?`, v, u.TenantID, id)
	}
	// Return updated schedule.
	var sc Schedule
	var enabled int
	err := db.QueryRowContext(r.Context(),
		`SELECT id, tenant_id, kind, name, cron_expr, target_id, enabled, last_run_at, next_run_at, created_at
		 FROM schedules WHERE tenant_id=? AND id=?`, u.TenantID, id).Scan(
		&sc.ID, &sc.TenantID, &sc.Kind, &sc.Name, &sc.CronExpr,
		&sc.TargetID, &enabled, &sc.LastRunAt, &sc.NextRunAt, &sc.CreatedAt)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	sc.Enabled = enabled != 0
	writeJSON(w, http.StatusOK, sc)
}

func (s *server) handleDeleteSchedule(w http.ResponseWriter, r *http.Request) {
	u := userFromContext(r.Context())
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	db, dbOK := s.DB.(*sql.DB)
	if !dbOK || db == nil {
		writeError(w, http.StatusInternalServerError, "database unavailable")
		return
	}
	res, err := db.ExecContext(r.Context(),
		`DELETE FROM schedules WHERE tenant_id=? AND id=?`, u.TenantID, id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		writeError(w, http.StatusNotFound, "schedule not found")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ============================================================

func readAllLimited(r *http.Request, max int64) ([]byte, error) {
	if r.ContentLength > max {
		return nil, errors.New("request too large")
	}
	body, err := io.ReadAll(http.MaxBytesReader(nil, r.Body, max))
	if err != nil {
		return nil, fmt.Errorf("read body: %w", err)
	}
	if len(body) == 0 {
		return nil, errors.New("empty body")
	}
	var raw any
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("invalid json: %w", err)
	}
	return body, nil
}
