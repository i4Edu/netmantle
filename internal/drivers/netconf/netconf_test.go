package netconf

import (
	"strings"
	"testing"
)

func TestGetConfigRPC(t *testing.T) {
	rpc := GetConfigRPC(101)
	if !strings.Contains(rpc, `message-id="101"`) || !strings.Contains(rpc, "<get-config>") {
		t.Fatalf("rpc: %s", rpc)
	}
	if !strings.HasSuffix(strings.TrimSpace(rpc), "]]>]]>") {
		t.Fatalf("not framed: %q", rpc)
	}
}

func TestParseRPCReply(t *testing.T) {
	in := `<rpc-reply><data><config><hostname>r1</hostname></config></data></rpc-reply>` + "]]>]]>"
	body, err := ParseRPCReply(in)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(body, "<hostname>r1</hostname>") {
		t.Fatalf("body: %q", body)
	}
	if _, err := ParseRPCReply("garbage]]>]]>"); err == nil {
		t.Fatal("expected error")
	}
}
