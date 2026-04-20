package transport

import (
	"encoding/base64"
	"testing"
)

func TestParseNetconfEditConfigCommandRejectsUnsafePayload(t *testing.T) {
	tests := []string{
		"<config>ok</config>]]>]]>",
		"</config><rpc>",
		"<rpc><edit-config/></rpc>",
	}
	for _, p := range tests {
		cmd := "edit-config:running:" + base64.StdEncoding.EncodeToString([]byte(p))
		if _, _, err := parseNetconfEditConfigCommand(cmd); err == nil {
			t.Fatalf("expected unsafe payload to be rejected: %q", p)
		}
	}
}

func TestParseNetconfEditConfigCommandAcceptsNormalPayload(t *testing.T) {
	payload := `<interfaces xmlns="urn:ietf:params:xml:ns:yang:ietf-interfaces"></interfaces>`
	cmd := "edit-config:running:" + base64.StdEncoding.EncodeToString([]byte(payload))
	ds, got, err := parseNetconfEditConfigCommand(cmd)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ds != "running" {
		t.Fatalf("unexpected datastore: %q", ds)
	}
	if got != payload {
		t.Fatalf("payload mismatch: %q", got)
	}
}
