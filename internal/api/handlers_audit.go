package api

import (
	"encoding/csv"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/i4Edu/netmantle/internal/audit"
)

// handleListAudit returns audit entries for the caller's tenant.
//
// Query parameters (all optional):
//
//	user=<id>             actor user id (exact)
//	action=<string>       action key (exact, e.g. "device.create")
//	target=<substring>    substring match on target (e.g. "device:42")
//	since=<RFC3339>       only entries at or after this time
//	until=<RFC3339>       only entries at or before this time
//	limit=<int>           max rows (default 100, max 500)
//	offset=<int>          pagination offset (default 0)
func (s *server) handleListAudit(w http.ResponseWriter, r *http.Request) {
	if s.Audit == nil {
		if r.URL.Query().Get("format") == "csv" {
			writeAuditCSV(w, nil)
			return
		}
		writeJSON(w, http.StatusOK, []audit.Entry{})
		return
	}
	u := userFromContext(r.Context())
	q := r.URL.Query()

	f := audit.ListFilter{TenantID: u.TenantID}
	if v := q.Get("user"); v != "" {
		id, err := strconv.ParseInt(v, 10, 64)
		if err != nil || id <= 0 {
			writeError(w, http.StatusBadRequest, "invalid user")
			return
		}
		f.ActorUserID = id
	}
	if v := q.Get("action"); v != "" {
		f.Action = v
	}
	if v := q.Get("target"); v != "" {
		f.Target = v
	}
	if v := q.Get("request_id"); v != "" {
		f.RequestID = v
	}
	if v := q.Get("since"); v != "" {
		t, err := parseRFC3339Loose(v)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid since (want RFC3339)")
			return
		}
		f.Since = t
	}
	if v := q.Get("until"); v != "" {
		t, err := parseRFC3339Loose(v)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid until (want RFC3339)")
			return
		}
		f.Until = t
	}
	if v := q.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n <= 0 || n > 500 {
			writeError(w, http.StatusBadRequest, "invalid limit (must be a positive integer, max 500)")
			return
		}
		f.Limit = n
	}
	if v := q.Get("offset"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 {
			writeError(w, http.StatusBadRequest, "invalid offset (must be a non-negative integer)")
			return
		}
		f.Offset = n
	}

	out, err := s.Audit.List(r.Context(), f)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if out == nil {
		out = []audit.Entry{}
	}
	if q.Get("format") == "csv" {
		writeAuditCSV(w, out)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// writeAuditCSV streams the same audit rows the JSON endpoint would return,
// in a spreadsheet-friendly form. The column set matches the Audit page so
// "what you see is what you export". Times are emitted as RFC3339Nano so the
// CSV round-trips losslessly back into filters.
//
// User-controlled string fields are sanitised against CSV/spreadsheet
// formula injection (cells beginning with `=`, `+`, `-` or `@` get a
// leading apostrophe so Excel/Sheets treat them as literal text).
func writeAuditCSV(w http.ResponseWriter, rows []audit.Entry) {
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition",
		fmt.Sprintf(`attachment; filename="netmantle-audit-%s.csv"`,
			time.Now().UTC().Format("20060102-150405")))
	cw := csv.NewWriter(w)
	if err := cw.Write([]string{
		"id", "created_at", "tenant_id", "actor_user_id",
		"source", "action", "target", "request_id", "detail",
	}); err != nil {
		http.Error(w, "failed to write csv", http.StatusInternalServerError)
		return
	}
	for _, e := range rows {
		if err := cw.Write([]string{
			strconv.FormatInt(e.ID, 10),
			e.CreatedAt.UTC().Format(time.RFC3339Nano),
			int64Ptr(e.TenantID),
			int64Ptr(e.ActorUserID),
			sanitizeCSVCell(e.Source),
			sanitizeCSVCell(e.Action),
			sanitizeCSVCell(e.Target),
			sanitizeCSVCell(e.RequestID),
			sanitizeCSVCell(e.Detail),
		}); err != nil {
			http.Error(w, "failed to write csv", http.StatusInternalServerError)
			return
		}
	}
	cw.Flush()
	if err := cw.Error(); err != nil {
		http.Error(w, "failed to write csv", http.StatusInternalServerError)
		return
	}
}

// sanitizeCSVCell defends against CSV/spreadsheet formula injection by
// prefixing user-controlled values that start with one of `=`, `+`, `-`,
// `@` (or a tab/CR which some apps treat as a continuation of the prior
// cell) with a single apostrophe. Excel and Sheets render the cell as
// literal text in that case. The original value still round-trips for
// machine consumers that re-parse the CSV.
func sanitizeCSVCell(v string) string {
	if v == "" {
		return v
	}
	switch v[0] {
	case '=', '+', '-', '@', '\t', '\r':
		return "'" + v
	default:
		return v
	}
}

func int64Ptr(p *int64) string {
	if p == nil {
		return ""
	}
	return strconv.FormatInt(*p, 10)
}

// parseRFC3339Loose accepts both RFC3339 and RFC3339Nano. The Audit page
// builds bounds via Date.toISOString() which always emits milliseconds, so
// strict RFC3339 parsing would reject every UI-driven request.
func parseRFC3339Loose(v string) (time.Time, error) {
	if t, err := time.Parse(time.RFC3339Nano, v); err == nil {
		return t, nil
	}
	return time.Parse(time.RFC3339, v)
}
