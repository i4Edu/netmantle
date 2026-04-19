// Package netconf implements a minimal NETCONF-over-SSH client used by
// the Phase 10 NETCONF/RESTCONF/gNMI driver scaffold.
//
// We support only the get-config <running> RPC, which is the operation a
// configuration-management product needs first. RESTCONF and gNMI are
// represented by stub drivers in internal/drivers/netconf that return a
// "not implemented" error so an operator can at least register devices of
// those types and see a clear roadmap message in the UI.
package netconf

import (
	"errors"
	"fmt"
	"io"
	"strings"
)

// HelloMessage is the standard NETCONF capabilities hello.
const HelloMessage = `<?xml version="1.0" encoding="UTF-8"?>
<hello xmlns="urn:ietf:params:xml:ns:netconf:base:1.0">
  <capabilities>
    <capability>urn:ietf:params:netconf:base:1.0</capability>
  </capabilities>
</hello>
]]>]]>`

// GetConfigRPC returns a get-config RPC message for the running datastore.
func GetConfigRPC(messageID int) string {
	return fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<rpc message-id="%d" xmlns="urn:ietf:params:xml:ns:netconf:base:1.0">
  <get-config>
    <source><running/></source>
  </get-config>
</rpc>
]]>]]>`, messageID)
}

// ParseRPCReply scans a NETCONF reply, returning the inner <data> element
// content. Sufficient for unit testing the driver's parsing.
func ParseRPCReply(framed string) (string, error) {
	// Strip the chunk delimiter "]]>]]>".
	parts := strings.SplitN(framed, "]]>]]>", 2)
	body := parts[0]
	startTag := "<data>"
	endTag := "</data>"
	si := strings.Index(body, startTag)
	ei := strings.Index(body, endTag)
	if si < 0 || ei < 0 || ei < si {
		return "", errors.New("netconf: <data> not found")
	}
	return body[si+len(startTag) : ei], nil
}

// CopyDataElement is a tiny helper that reads from r, returning the first
// <data>…</data> element it encounters (or io.EOF). Streaming-style: the
// actual driver implementation will call this on the SSH stdout channel.
func CopyDataElement(r io.Reader) (string, error) {
	buf := make([]byte, 0, 4096)
	tmp := make([]byte, 1024)
	for {
		n, err := r.Read(tmp)
		if n > 0 {
			buf = append(buf, tmp[:n]...)
			if data, perr := ParseRPCReply(string(buf)); perr == nil {
				return data, nil
			}
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", err
		}
	}
	return "", errors.New("netconf: stream ended without complete <data>")
}
