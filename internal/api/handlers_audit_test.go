package api

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

// TestAuditAndCredentialSecret verifies:
//   - mutating handlers (credential.create, device.create, device.delete,
//     device.backup.request) write rows to audit_log via GET /api/v1/audit
//   - filters on `action`, `target`, `user`, `limit` work
//   - the credential-list and credential-create responses do NOT leak the
//     plaintext secret (zero-credential-access regression)
func TestAuditAndCredentialSecret(t *testing.T) {
	srv, pw := newTestServer(t)
	defer srv.Close()
	c := login(t, srv, pw)

	const secret = "supersecret-pw-do-not-leak"

	// Create credential — secret in request body, must NOT come back in body.
	credBody, _ := json.Marshal(map[string]string{
		"name": "lab", "username": "u", "secret": secret,
	})
	resp, _ := c.Post(srv.URL+"/api/v1/credentials", "application/json",
		bytes.NewReader(credBody))
	if resp.StatusCode != 201 {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("cred create: %s %s", resp.Status, b)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if bytes.Contains(body, []byte(secret)) {
		t.Fatalf("credential create response leaked secret: %s", body)
	}
	var cred map[string]any
	_ = json.Unmarshal(body, &cred)
	credID := int64(cred["id"].(float64))

	// List credentials — must not contain secret.
	resp, _ = c.Get(srv.URL + "/api/v1/credentials")
	body, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if bytes.Contains(body, []byte(secret)) {
		t.Fatalf("credential list leaked secret: %s", body)
	}

	// Create device.
	devBody, _ := json.Marshal(map[string]any{
		"hostname": "r1", "address": "10.0.0.1", "port": 22,
		"driver": "cisco_ios", "credential_id": credID,
	})
	resp, _ = c.Post(srv.URL+"/api/v1/devices", "application/json",
		bytes.NewReader(devBody))
	if resp.StatusCode != 201 {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("device create: %s %s", resp.Status, b)
	}
	var dev map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&dev)
	resp.Body.Close()
	devID := int64(dev["id"].(float64))

	// Backup-now (drives the synchronous backup path → also writes its own
	// audit row through the backup service, separate from the API request
	// audit).
	req, _ := http.NewRequest(http.MethodPost,
		srv.URL+"/api/v1/devices/"+itoa(devID)+"/backup", nil)
	resp, _ = c.Do(req)
	resp.Body.Close()

	// Delete device.
	req, _ = http.NewRequest(http.MethodDelete,
		srv.URL+"/api/v1/devices/"+itoa(devID), nil)
	resp, _ = c.Do(req)
	resp.Body.Close()

	// List audit (no filter).
	resp, _ = c.Get(srv.URL + "/api/v1/audit")
	if resp.StatusCode != 200 {
		t.Fatalf("audit list: %s", resp.Status)
	}
	body, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	var entries []map[string]any
	_ = json.Unmarshal(body, &entries)
	// Audit body must never echo the secret.
	if bytes.Contains(body, []byte(secret)) {
		t.Fatalf("audit log leaked secret: %s", body)
	}
	gotActions := map[string]int{}
	for _, e := range entries {
		gotActions[e["action"].(string)]++
	}
	for _, want := range []string{
		"credential.create", "device.create", "device.delete",
		"device.backup.request", "device.backup",
	} {
		if gotActions[want] == 0 {
			t.Fatalf("missing audit action %q in %v", want, gotActions)
		}
	}

	// Filter by action.
	resp, _ = c.Get(srv.URL + "/api/v1/audit?action=device.create")
	body, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	entries = nil
	_ = json.Unmarshal(body, &entries)
	if len(entries) != 1 || entries[0]["action"] != "device.create" {
		t.Fatalf("filter action: %s", body)
	}

	// Filter by target substring.
	resp, _ = c.Get(srv.URL + "/api/v1/audit?target=device:" + itoa(devID))
	body, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	entries = nil
	_ = json.Unmarshal(body, &entries)
	if len(entries) < 2 {
		t.Fatalf("filter target: want >=2 (create+delete+backup), got %d: %s",
			len(entries), body)
	}
	for _, e := range entries {
		if !strings.Contains(e["target"].(string), "device:"+itoa(devID)) {
			t.Fatalf("filter target leaked %v", e)
		}
	}

	// Filter by user (admin is id 1 — created by EnsureBootstrapAdmin).
	resp, _ = c.Get(srv.URL + "/api/v1/audit?user=1&limit=3")
	body, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	entries = nil
	_ = json.Unmarshal(body, &entries)
	if len(entries) == 0 || len(entries) > 3 {
		t.Fatalf("filter user+limit: %d entries", len(entries))
	}
	for _, e := range entries {
		if v, ok := e["actor_user_id"].(float64); !ok || int64(v) != 1 {
			t.Fatalf("user filter mismatch: %v", e)
		}
	}

	// Bad filter values are rejected.
	for _, bad := range []string{
		"/api/v1/audit?user=abc",
		"/api/v1/audit?since=not-a-date",
		"/api/v1/audit?limit=-1",
	} {
		resp, _ = c.Get(srv.URL + bad)
		if resp.StatusCode != 400 {
			t.Fatalf("%s: want 400, got %s", bad, resp.Status)
		}
		resp.Body.Close()
	}
}
