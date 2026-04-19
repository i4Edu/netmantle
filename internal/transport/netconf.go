package transport

import (
	"context"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"time"

	netconfpkg "github.com/i4Edu/netmantle/internal/drivers/netconf"
	"golang.org/x/crypto/ssh"
)

// NetconfConfig holds the connection parameters for a NETCONF-over-SSH
// session (RFC 6242 — NETCONF over SSH).
type NetconfConfig struct {
	Address  string
	Port     int // defaults to 830 (NETCONF standard port)
	Username string
	Password string
	// KnownHosts pins host keys. When nil a temporary MemKnownHostsStore is
	// used (TOFU). Provide a DBKnownHostsStore in production to persist pins.
	KnownHosts KnownHostsStore
	TenantID   int64
	Timeout    time.Duration
}

// NetconfSession implements drivers.Session over a NETCONF subsystem channel.
// Commands are interpreted as NETCONF datastore names:
//
//   - "get-config:running"   → <get-config><source><running/></source></get-config>
//   - "get-config:candidate" → <get-config><source><candidate/></source></get-config>
//
// The returned string is the raw <data> element content from the RPC reply.
// This lets builtin CLI drivers (cisco_netconf, junos_netconf) call
// sess.Run("get-config:running") without knowing the wire details.
type NetconfSession struct {
	client  *ssh.Client
	session *ssh.Session
	stdin   io.WriteCloser
	stdout  io.Reader
	msgID   int
	timeout time.Duration
}

// DialNetconf opens a NETCONF-over-SSH subsystem session (RFC 6242).
// It returns a *NetconfSession, a closer function, and any error.
func DialNetconf(ctx context.Context, cfg NetconfConfig) (*NetconfSession, func() error, error) {
	if cfg.Username == "" {
		return nil, nil, fmt.Errorf("transport/netconf: empty username")
	}
	if cfg.Timeout == 0 {
		cfg.Timeout = 30 * time.Second
	}
	port := cfg.Port
	if port == 0 {
		port = 830 // IANA-assigned NETCONF port
	}

	addr := cfg.Address
	if _, _, err := net.SplitHostPort(addr); err != nil {
		addr = net.JoinHostPort(addr, strconv.Itoa(port))
	}

	sshCfg := SSHConfig{
		Address:    cfg.Address,
		Port:       port,
		Username:   cfg.Username,
		Password:   cfg.Password,
		KnownHosts: cfg.KnownHosts,
		TenantID:   cfg.TenantID,
		Timeout:    cfg.Timeout,
	}
	hk := resolveHostKeyCallback(ctx, sshCfg)

	clientCfg := &ssh.ClientConfig{
		User:            cfg.Username,
		Auth:            []ssh.AuthMethod{ssh.Password(cfg.Password)},
		HostKeyCallback: hk,
		Timeout:         cfg.Timeout,
	}

	d := net.Dialer{Timeout: cfg.Timeout}
	netConn, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, nil, fmt.Errorf("transport/netconf: dial: %w", err)
	}
	conn, chans, reqs, err := ssh.NewClientConn(netConn, addr, clientCfg)
	if err != nil {
		_ = netConn.Close()
		return nil, nil, fmt.Errorf("transport/netconf: handshake: %w", err)
	}
	client := ssh.NewClient(conn, chans, reqs)

	sess, err := client.NewSession()
	if err != nil {
		_ = client.Close()
		return nil, nil, fmt.Errorf("transport/netconf: session: %w", err)
	}

	stdin, err := sess.StdinPipe()
	if err != nil {
		_ = sess.Close()
		_ = client.Close()
		return nil, nil, fmt.Errorf("transport/netconf: stdin: %w", err)
	}
	stdout, err := sess.StdoutPipe()
	if err != nil {
		_ = sess.Close()
		_ = client.Close()
		return nil, nil, fmt.Errorf("transport/netconf: stdout: %w", err)
	}

	// Request the netconf subsystem as required by RFC 6242 §3.
	if err := sess.RequestSubsystem("netconf"); err != nil {
		_ = sess.Close()
		_ = client.Close()
		return nil, nil, fmt.Errorf("transport/netconf: request subsystem: %w", err)
	}

	s := &NetconfSession{
		client:  client,
		session: sess,
		stdin:   stdin,
		stdout:  stdout,
		msgID:   1,
		timeout: cfg.Timeout,
	}

	// Exchange capability hellos.
	if err := s.handshake(ctx); err != nil {
		_ = sess.Close()
		_ = client.Close()
		return nil, nil, fmt.Errorf("transport/netconf: hello exchange: %w", err)
	}

	closer := func() error {
		// Send a <close-session> before dropping the connection so the
		// device can clean up its NETCONF session gracefully.
		_ = s.sendClose()
		_ = sess.Close()
		return client.Close()
	}
	return s, closer, nil
}

// handshake sends our hello and consumes the device's hello.
func (s *NetconfSession) handshake(ctx context.Context) error {
	// Send our capabilities hello.
	if _, err := io.WriteString(s.stdin, netconfpkg.HelloMessage+"\n"); err != nil {
		return fmt.Errorf("send hello: %w", err)
	}
	// Read and discard the device hello — we only need the ]]>]]> delimiter
	// to know the hello is complete.
	buf := make([]byte, 0, 4096)
	tmp := make([]byte, 1024)
	deadline := time.Now().Add(s.timeout)
	for {
		if time.Now().After(deadline) {
			return fmt.Errorf("hello timeout")
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		n, err := s.stdout.Read(tmp)
		if n > 0 {
			buf = append(buf, tmp[:n]...)
			if strings.Contains(string(buf), "]]>]]>") {
				return nil
			}
		}
		if err != nil {
			if err == io.EOF {
				break
			}
			return err
		}
	}
	return fmt.Errorf("connection closed before hello complete")
}

// Run interprets cmd as a NETCONF datastore selector and returns the
// <data> content of the get-config reply.
//
// Supported commands:
//
//	"get-config:running"   – retrieve the running datastore (default)
//	"get-config:candidate" – retrieve the candidate datastore
//	"get-config"           – shorthand for "get-config:running"
func (s *NetconfSession) Run(ctx context.Context, cmd string) (string, error) {
	cmd = strings.TrimSpace(cmd)

	// Determine datastore from command string.
	var datastore string
	switch {
	case cmd == "get-config" || cmd == "get-config:running":
		datastore = "running"
	case cmd == "get-config:candidate":
		datastore = "candidate"
	case cmd == "get-config:startup":
		datastore = "startup"
	default:
		return "", fmt.Errorf("transport/netconf: unsupported command %q (use get-config[:running|candidate|startup])", cmd)
	}

	rpc := buildGetConfigRPC(s.msgID, datastore)
	s.msgID++

	if _, err := io.WriteString(s.stdin, rpc); err != nil {
		return "", fmt.Errorf("transport/netconf: send rpc: %w", err)
	}

	// Read the full reply, bounded by ]]>]]>.
	buf := make([]byte, 0, 16384)
	tmp := make([]byte, 4096)
	deadline := time.Now().Add(s.timeout)
	for {
		if time.Now().After(deadline) {
			return "", fmt.Errorf("transport/netconf: reply timeout")
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		default:
		}
		n, err := s.stdout.Read(tmp)
		if n > 0 {
			buf = append(buf, tmp[:n]...)
			if strings.Contains(string(buf), "]]>]]>") {
				break
			}
		}
		if err != nil {
			if err == io.EOF {
				break
			}
			return "", fmt.Errorf("transport/netconf: read reply: %w", err)
		}
	}

	data, err := netconfpkg.ParseRPCReply(string(buf))
	if err != nil {
		return "", fmt.Errorf("transport/netconf: parse reply: %w", err)
	}
	return strings.TrimSpace(data), nil
}

// sendClose sends a NETCONF <close-session> RPC. Errors are ignored.
func (s *NetconfSession) sendClose() error {
	const closeRPC = `<?xml version="1.0" encoding="UTF-8"?>
<rpc message-id="9999" xmlns="urn:ietf:params:xml:ns:netconf:base:1.0">
  <close-session/>
</rpc>
]]>]]>`
	_, err := io.WriteString(s.stdin, closeRPC)
	return err
}

// buildGetConfigRPC assembles a get-config RPC for the given datastore.
func buildGetConfigRPC(msgID int, datastore string) string {
	return fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<rpc message-id="%d" xmlns="urn:ietf:params:xml:ns:netconf:base:1.0">
  <get-config>
    <source><%s/></source>
  </get-config>
</rpc>
]]>]]>`, msgID, datastore)
}
