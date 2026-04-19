package api

import (
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
	if v := q.Get("since"); v != "" {
		t, err := time.Parse(time.RFC3339, v)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid since (want RFC3339)")
			return
		}
		f.Since = t
	}
	if v := q.Get("until"); v != "" {
		t, err := time.Parse(time.RFC3339, v)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid until (want RFC3339)")
			return
		}
		f.Until = t
	}
	if v := q.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n <= 0 {
			writeError(w, http.StatusBadRequest, "invalid limit")
			return
		}
		f.Limit = n
	}
	if v := q.Get("offset"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 {
			writeError(w, http.StatusBadRequest, "invalid offset")
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
	writeJSON(w, http.StatusOK, out)
}
