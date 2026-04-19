// Package transport contains transport-layer adapters that produce
// drivers.Session implementations. Phase 1 ships SSH only.
package transport

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"regexp"
	"strconv"
	"sync"
	"time"

	"github.com/i4Edu/netmantle/internal/drivers"
	"golang.org/x/crypto/ssh"
)

// SSHConfig holds connection parameters for an SSH session.
type SSHConfig struct {
	Address  string // host or host:port
	Port     int    // used if Address has no port
	Username string
	Password string
	// HostKeyCallback. If nil, host keys are accepted on first use within
	// the lifetime of the process (in-memory TOFU). A persistent
	// known_hosts store will land in a follow-up.
	HostKeyCallback ssh.HostKeyCallback
	Timeout         time.Duration
}

// DialSSH opens an interactive SSH session and returns a drivers.Session
// that runs commands by writing to the shell, consuming output until the
// device prompt is seen again.
func DialSSH(ctx context.Context, cfg SSHConfig) (drivers.Session, func() error, error) {
	if cfg.Username == "" {
		return nil, nil, errors.New("transport/ssh: empty username")
	}
	if cfg.Timeout == 0 {
		cfg.Timeout = 30 * time.Second
	}

	addr := cfg.Address
	if _, _, err := net.SplitHostPort(addr); err != nil {
		port := cfg.Port
		if port == 0 {
			port = 22
		}
		addr = net.JoinHostPort(addr, strconv.Itoa(port))
	}

	hk := cfg.HostKeyCallback
	if hk == nil {
		hk = tofuHostKey()
	}
	clientCfg := &ssh.ClientConfig{
		User:            cfg.Username,
		Auth:            []ssh.AuthMethod{ssh.Password(cfg.Password)},
		HostKeyCallback: hk,
		Timeout:         cfg.Timeout,
	}

	d := net.Dialer{Timeout: cfg.Timeout}
	netConn, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, nil, fmt.Errorf("transport/ssh: dial: %w", err)
	}
	conn, chans, reqs, err := ssh.NewClientConn(netConn, addr, clientCfg)
	if err != nil {
		_ = netConn.Close()
		return nil, nil, fmt.Errorf("transport/ssh: handshake: %w", err)
	}
	client := ssh.NewClient(conn, chans, reqs)

	sess, err := client.NewSession()
	if err != nil {
		_ = client.Close()
		return nil, nil, fmt.Errorf("transport/ssh: session: %w", err)
	}
	stdin, err := sess.StdinPipe()
	if err != nil {
		_ = sess.Close()
		_ = client.Close()
		return nil, nil, err
	}
	stdout, err := sess.StdoutPipe()
	if err != nil {
		_ = sess.Close()
		_ = client.Close()
		return nil, nil, err
	}
	// Request a non-tty PTY to discourage paging/colour, but don't fail if
	// the server refuses (common on appliances).
	_ = sess.RequestPty("vt100", 80, 200, ssh.TerminalModes{
		ssh.ECHO:          0,
		ssh.TTY_OP_ISPEED: 14400,
		ssh.TTY_OP_OSPEED: 14400,
	})
	if err := sess.Shell(); err != nil {
		_ = sess.Close()
		_ = client.Close()
		return nil, nil, fmt.Errorf("transport/ssh: shell: %w", err)
	}

	s := &sshSession{
		client:  client,
		session: sess,
		stdin:   stdin,
		stdout:  stdout,
		timeout: cfg.Timeout,
	}
	// Discard the initial banner / first prompt.
	_, _ = s.readUntilPrompt(ctx)

	closer := func() error {
		_ = sess.Close()
		return client.Close()
	}
	return s, closer, nil
}

// shellChannel is an io.ReadWriteCloser bridging an SSH shell's stdin and
// stdout. It is used by the in-app web terminal (Phase 7).
type shellChannel struct {
	client  *ssh.Client
	session *ssh.Session
	stdin   io.WriteCloser
	stdout  io.Reader
}

func (c *shellChannel) Read(p []byte) (int, error)  { return c.stdout.Read(p) }
func (c *shellChannel) Write(p []byte) (int, error) { return c.stdin.Write(p) }
func (c *shellChannel) Close() error {
	_ = c.session.Close()
	return c.client.Close()
}

// DialSSHShell opens an interactive shell suitable for raw byte-level
// proxying (the in-app CLI uses this). Unlike DialSSH, it requests a real
// PTY and does no prompt detection — input/output are forwarded verbatim.
func DialSSHShell(ctx context.Context, cfg SSHConfig) (io.ReadWriteCloser, error) {
	if cfg.Username == "" {
		return nil, errors.New("transport/ssh: empty username")
	}
	if cfg.Timeout == 0 {
		cfg.Timeout = 30 * time.Second
	}
	addr := cfg.Address
	if _, _, err := net.SplitHostPort(addr); err != nil {
		port := cfg.Port
		if port == 0 {
			port = 22
		}
		addr = net.JoinHostPort(addr, strconv.Itoa(port))
	}
	hk := cfg.HostKeyCallback
	if hk == nil {
		hk = tofuHostKey()
	}
	clientCfg := &ssh.ClientConfig{
		User:            cfg.Username,
		Auth:            []ssh.AuthMethod{ssh.Password(cfg.Password)},
		HostKeyCallback: hk,
		Timeout:         cfg.Timeout,
	}
	d := net.Dialer{Timeout: cfg.Timeout}
	netConn, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("transport/ssh: dial: %w", err)
	}
	conn, chans, reqs, err := ssh.NewClientConn(netConn, addr, clientCfg)
	if err != nil {
		_ = netConn.Close()
		return nil, fmt.Errorf("transport/ssh: handshake: %w", err)
	}
	client := ssh.NewClient(conn, chans, reqs)
	sess, err := client.NewSession()
	if err != nil {
		_ = client.Close()
		return nil, fmt.Errorf("transport/ssh: session: %w", err)
	}
	stdin, err := sess.StdinPipe()
	if err != nil {
		_ = sess.Close()
		_ = client.Close()
		return nil, err
	}
	stdout, err := sess.StdoutPipe()
	if err != nil {
		_ = sess.Close()
		_ = client.Close()
		return nil, err
	}
	if err := sess.RequestPty("xterm", 40, 120, ssh.TerminalModes{}); err != nil {
		_ = sess.Close()
		_ = client.Close()
		return nil, fmt.Errorf("transport/ssh: pty: %w", err)
	}
	if err := sess.Shell(); err != nil {
		_ = sess.Close()
		_ = client.Close()
		return nil, fmt.Errorf("transport/ssh: shell: %w", err)
	}
	return &shellChannel{client: client, session: sess, stdin: stdin, stdout: stdout}, nil
}

// sshSession implements drivers.Session over an interactive SSH shell.
type sshSession struct {
	client  *ssh.Client
	session *ssh.Session
	stdin   io.WriteCloser
	stdout  io.Reader
	timeout time.Duration
	mu      sync.Mutex
}

// promptRE matches typical CLI prompts at end-of-output. We accept
// hostname#, hostname>, hostname$ and hostname(config)# variants.
var promptRE = regexp.MustCompile(`(?m)^\S+(\([^)]+\))?[#>$]\s*$`)

func (s *sshSession) Run(ctx context.Context, cmd string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, err := io.WriteString(s.stdin, cmd+"\n"); err != nil {
		return "", fmt.Errorf("transport/ssh: write: %w", err)
	}
	out, err := s.readUntilPrompt(ctx)
	if err != nil {
		return "", err
	}
	return cleanCommandEcho(out, cmd), nil
}

func (s *sshSession) readUntilPrompt(ctx context.Context) (string, error) {
	deadline, hasDeadline := ctx.Deadline()
	if !hasDeadline {
		deadline = time.Now().Add(s.timeout)
	}

	var buf bytes.Buffer
	chunk := make([]byte, 4096)
	for {
		if time.Now().After(deadline) {
			return buf.String(), errors.New("transport/ssh: prompt timeout")
		}
		// Use a short read deadline by scheduling a goroutine with select.
		readCh := make(chan readResult, 1)
		go func() {
			n, err := s.stdout.Read(chunk)
			readCh <- readResult{n: n, err: err, data: append([]byte(nil), chunk[:n]...)}
		}()
		select {
		case <-ctx.Done():
			return buf.String(), ctx.Err()
		case <-time.After(time.Until(deadline)):
			return buf.String(), errors.New("transport/ssh: prompt timeout")
		case r := <-readCh:
			if r.n > 0 {
				buf.Write(r.data)
				if promptRE.Match(tail(buf.Bytes(), 256)) {
					return buf.String(), nil
				}
			}
			if r.err != nil {
				if r.err == io.EOF {
					return buf.String(), nil
				}
				return buf.String(), r.err
			}
		}
	}
}

type readResult struct {
	n    int
	err  error
	data []byte
}

func tail(b []byte, n int) []byte {
	if len(b) <= n {
		return b
	}
	return b[len(b)-n:]
}

// cleanCommandEcho removes the echoed command from the start and the
// trailing prompt line from the end of the captured output.
func cleanCommandEcho(raw, cmd string) string {
	out := raw
	// Strip the first occurrence of the echoed command + newline.
	if i := bytes.Index([]byte(out), []byte(cmd)); i >= 0 {
		j := i + len(cmd)
		if j < len(out) && (out[j] == '\n' || out[j] == '\r') {
			j++
			if j < len(out) && out[j] == '\n' {
				j++
			}
		}
		out = out[j:]
	}
	// Strip the trailing prompt line.
	if loc := promptRE.FindStringIndex(out); loc != nil {
		out = out[:loc[0]]
	}
	return out
}

// tofuHostKey returns a callback that accepts and pins host keys on first
// use, in memory only. This is acceptable for Phase 1 lab use; a persistent
// known_hosts file lands in a follow-up. The callback is concurrency-safe.
func tofuHostKey() ssh.HostKeyCallback {
	var mu sync.Mutex
	pinned := map[string][]byte{}
	return func(hostname string, remote net.Addr, key ssh.PublicKey) error {
		mu.Lock()
		defer mu.Unlock()
		k := key.Marshal()
		if existing, ok := pinned[hostname]; ok {
			if !bytes.Equal(existing, k) {
				return fmt.Errorf("transport/ssh: host key changed for %s", hostname)
			}
			return nil
		}
		pinned[hostname] = k
		return nil
	}
}
