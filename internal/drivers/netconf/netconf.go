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
	"encoding/xml"
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

// ParseRPCReply scans a NETCONF reply and returns the inner <data> element
// content. It uses encoding/xml token parsing so it handles namespace-
// qualified tags (e.g. <data xmlns="urn:ietf:params:xml:ns:netconf:base:1.0">)
// correctly, as required when talking to real RFC 6242-compliant devices.
func ParseRPCReply(framed string) (string, error) {
	// Strip the chunk delimiter "]]>]]>".
	body := strings.SplitN(framed, "]]>]]>", 2)[0]

	dec := xml.NewDecoder(strings.NewReader(body))
	var depth int
	var inData bool
	var builder strings.Builder

	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			// Tolerate partial/trailing content after the useful XML.
			break
		}
		switch t := tok.(type) {
		case xml.StartElement:
			if t.Name.Local == "data" {
				inData = true
				depth = 0
			} else if inData {
				depth++
				// Reconstruct the opening tag, stripping namespace declarations.
				// Go's encoding/xml represents namespace declarations as:
				//   xmlns="uri"       → Attr{Name:{Space:"", Local:"xmlns"}, ...}
				//   xmlns:prefix="uri"→ Attr{Name:{Space:"xmlns", Local:"prefix"}, ...}
				// Both patterns are excluded below; all other attributes are kept.
				builder.WriteString("<")
				builder.WriteString(t.Name.Local)
				for _, a := range t.Attr {
					if a.Name.Space == "xmlns" || a.Name.Local == "xmlns" {
						continue
					}
					fmt.Fprintf(&builder, " %s=%q", a.Name.Local, a.Value)
				}
				builder.WriteString(">")
			}
		case xml.EndElement:
			if inData {
				if t.Name.Local == "data" && depth == 0 {
					// End of <data> element — we have everything.
					return builder.String(), nil
				}
				depth--
				builder.WriteString("</")
				builder.WriteString(t.Name.Local)
				builder.WriteString(">")
			}
		case xml.CharData:
			if inData {
				builder.Write(t)
			}
		}
	}
	if inData {
		// We entered <data> but never saw the closing tag (truncated reply).
		return builder.String(), nil
	}
	return "", errors.New("netconf: <data> not found in RPC reply")
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
