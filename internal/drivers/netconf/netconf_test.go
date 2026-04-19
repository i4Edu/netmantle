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

// TestParseRPCReplyNamespace verifies that ParseRPCReply handles namespace-
// qualified <data> elements as returned by real RFC 6242-compliant devices.
func TestParseRPCReplyNamespace(t *testing.T) {
	cases := []struct {
		name  string
		input string
		want  string
	}{
		{
			name: "namespace on data element",
			input: `<?xml version="1.0"?>` +
				`<rpc-reply xmlns="urn:ietf:params:xml:ns:netconf:base:1.0">` +
				`<data xmlns="urn:ietf:params:xml:ns:netconf:base:1.0">` +
				`<config><hostname>router1</hostname></config>` +
				`</data></rpc-reply>` + "]]>]]>",
			want: "router1",
		},
		{
			name:  "no namespace (plain)",
			input: `<rpc-reply><data><config><hostname>r2</hostname></config></data></rpc-reply>` + "]]>]]>",
			want:  "r2",
		},
		{
			name: "namespace prefix",
			input: `<nc:rpc-reply xmlns:nc="urn:ietf:params:xml:ns:netconf:base:1.0">` +
				`<nc:data><config><hostname>r3</hostname></config></nc:data>` +
				`</nc:rpc-reply>` + "]]>]]>",
			want: "r3",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body, err := ParseRPCReply(tc.input)
			if err != nil {
				t.Fatalf("ParseRPCReply: %v", err)
			}
			if !strings.Contains(body, tc.want) {
				t.Errorf("body %q does not contain %q", body, tc.want)
			}
		})
	}
}
