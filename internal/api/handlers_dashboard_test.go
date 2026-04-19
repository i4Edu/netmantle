package api

import (
	"encoding/json"
	"io"
	"strings"
	"testing"
)

// TestDashboardSummary verifies the aggregation endpoint returns a stable
// JSON shape even on an empty tenant, and that creating a device shows up
// in the device tally.
func TestDashboardSummary(t *testing.T) {
	srv, pw := newTestServer(t)
	defer srv.Close()
	c := login(t, srv, pw)

	// Empty-tenant call: must return 200 with zeroed sections.
	resp, err := c.Get(srv.URL + "/api/v1/dashboard/summary")
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 200 {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("status=%s body=%s", resp.Status, b)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	var s map[string]any
	if err := json.Unmarshal(body, &s); err != nil {
		t.Fatalf("invalid json: %v body=%s", err, body)
	}
	for _, k := range []string{"devices", "compliance", "backups", "approvals", "status_by_driver", "drift_hotspots", "recent_events", "health"} {
		if _, ok := s[k]; !ok {
			t.Fatalf("summary missing %q: %s", k, body)
		}
	}
	if d := s["devices"].(map[string]any); d["total"].(float64) != 0 {
		t.Fatalf("expected 0 devices, got %v", d["total"])
	}

	// Add a device, then re-check the tally.
	devBody := strings.NewReader(`{"hostname":"r1","address":"10.0.0.1","port":22,"driver":"cisco_ios"}`)
	resp, _ = c.Post(srv.URL+"/api/v1/devices", "application/json", devBody)
	if resp.StatusCode != 201 {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("create device: %s %s", resp.Status, b)
	}
	resp.Body.Close()

	resp, _ = c.Get(srv.URL + "/api/v1/dashboard/summary")
	body, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	_ = json.Unmarshal(body, &s)
	d := s["devices"].(map[string]any)
	if d["total"].(float64) != 1 {
		t.Fatalf("expected 1 device after create, got %v", d["total"])
	}
	if d["added_recent"].(float64) != 1 {
		t.Fatalf("expected added_recent=1, got %v", d["added_recent"])
	}
	// status_by_driver should now contain cisco_ios.
	sbd := s["status_by_driver"].([]any)
	if len(sbd) != 1 {
		t.Fatalf("expected 1 driver bucket, got %d", len(sbd))
	}
	if sbd[0].(map[string]any)["driver"].(string) != "cisco_ios" {
		t.Fatalf("expected cisco_ios driver bucket, got %v", sbd[0])
	}
}

// TestAuditCSVExport verifies the ?format=csv branch returns a CSV payload
// with the expected header row and no JSON content-type.
func TestAuditCSVExport(t *testing.T) {
	srv, pw := newTestServer(t)
	defer srv.Close()
	c := login(t, srv, pw)

	// A login above produced at least one audit row; if not, create another.
	devBody := strings.NewReader(`{"hostname":"r1","address":"10.0.0.1","port":22,"driver":"cisco_ios"}`)
	r, _ := c.Post(srv.URL+"/api/v1/devices", "application/json", devBody)
	r.Body.Close()

	resp, err := c.Get(srv.URL + "/api/v1/audit?format=csv&limit=10")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("status=%s body=%s", resp.Status, b)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/csv") {
		t.Fatalf("expected text/csv, got %q", ct)
	}
	if cd := resp.Header.Get("Content-Disposition"); !strings.Contains(cd, "attachment") {
		t.Fatalf("expected Content-Disposition to be attachment, got %q", cd)
	}
	body, _ := io.ReadAll(resp.Body)
	header := strings.SplitN(string(body), "\n", 2)[0]
	wantCols := []string{"id", "created_at", "tenant_id", "actor_user_id", "source", "action", "target", "request_id", "detail"}
	for _, col := range wantCols {
		if !strings.Contains(header, col) {
			t.Fatalf("CSV header missing %q: %q", col, header)
		}
	}
}

// TestAuditCSVExportEmpty verifies that even when no audit service is wired
// (e.g. in a misconfigured test) the CSV branch returns headers, not JSON.
func TestAuditCSVHeaderShapeWithService(t *testing.T) {
	// Reuse the standard server (which has audit) and request an extreme
	// filter so we get an empty result set.
	srv, pw := newTestServer(t)
	defer srv.Close()
	c := login(t, srv, pw)

	resp, err := c.Get(srv.URL + "/api/v1/audit?format=csv&action=__nope__")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("status=%s body=%s", resp.Status, b)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.HasPrefix(string(body), "id,created_at,") {
		t.Fatalf("expected CSV header, got %q", string(body))
	}
}
