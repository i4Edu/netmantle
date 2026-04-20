package transport_test

import (
	"context"
	"fmt"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/i4Edu/netmantle/internal/transport"
)

// fakeTelnetServer simulates a minimal Telnet server:
//   - sends IAC WILL ECHO (option 1) on connect to exercise IAC negotiation
//   - sends a "login:" prompt
//   - accepts any username, then sends "Password:"
//   - accepts any password, sends a router# prompt
//   - echoes commands back followed by a fake output and the prompt again
func startFakeTelnetServer(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { ln.Close() })

	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		conn.SetDeadline(time.Now().Add(10 * time.Second)) //nolint:errcheck

		// IAC WILL ECHO, IAC DO SUPPRESS-GO-AHEAD
		conn.Write([]byte{0xFF, 0xFB, 0x01, 0xFF, 0xFD, 0x03}) //nolint:errcheck
		// Drain any IAC responses the client sends back (up to 32 bytes)
		buf := make([]byte, 32)
		conn.SetReadDeadline(time.Now().Add(500 * time.Millisecond)) //nolint:errcheck
		conn.Read(buf)                                               //nolint:errcheck
		conn.SetReadDeadline(time.Now().Add(10 * time.Second))       //nolint:errcheck

		conn.Write([]byte("login: ")) //nolint:errcheck
		readLine(conn)

		conn.Write([]byte("Password: ")) //nolint:errcheck
		readLine(conn)

		conn.Write([]byte("router# ")) //nolint:errcheck

		// Handle one command.
		cmd := readLine(conn)
		cmd = strings.TrimRight(cmd, "\r\n ")
		conn.Write([]byte(cmd + "\r\nfake output line\r\nrouter# ")) //nolint:errcheck
	}()

	return ln.Addr().String()
}

func readLine(conn net.Conn) string {
	var sb strings.Builder
	buf := make([]byte, 1)
	for {
		n, err := conn.Read(buf)
		if n > 0 {
			sb.WriteByte(buf[0])
			if buf[0] == '\n' {
				break
			}
		}
		if err != nil {
			break
		}
	}
	return sb.String()
}

func TestDialTelnet_LoginAndRun(t *testing.T) {
	addr := startFakeTelnetServer(t)
	host, portStr, _ := net.SplitHostPort(addr)
	var port int
	fmt.Sscan(portStr, &port)
	_ = portStr

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Import the package under test via the external test package.
	// We call it indirectly through the existing helper to avoid an import cycle.
	// Instead, we use a direct call via the transport package alias.
	sess, closer, err := transport.DialTelnet(ctx, transport.TelnetConfig{
		Address:  host,
		Port:     port,
		Username: "admin",
		Password: "secret",
		Timeout:  8 * time.Second,
	})
	if err != nil {
		t.Fatalf("DialTelnet: %v", err)
	}
	defer closer() //nolint:errcheck

	out, err := sess.Run(ctx, "show version")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !strings.Contains(out, "fake output") {
		t.Errorf("expected 'fake output' in output, got: %q", out)
	}
}
