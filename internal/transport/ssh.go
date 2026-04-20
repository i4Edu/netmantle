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
	// HostKeyCallback overrides the default known-hosts behaviour when set.
	// If nil, KnownHosts is consulted instead; if both are nil, keys are
	// accepted and pinned in an in-process MemKnownHostsStore (TOFU).
	HostKeyCallback ssh.HostKeyCallback
	// KnownHosts is the persistent store used for SSH host-key pinning.
	// Prefer this over HostKeyCallback — it persists across process
	// restarts (closing threat-model gap T7). When nil, a temporary
	// MemKnownHostsStore is created for the lifetime of the dial call.
	KnownHosts KnownHostsStore
	// TenantID scopes the KnownHosts lookup so different tenants cannot
	// see each other's pinned keys.
	TenantID int64
	Timeout  time.Duration
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

	hk := resolveHostKeyCallback(ctx, cfg)
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

// DialSSHExec returns a drivers.Session that runs each command via SSH exec
// (non-interactive, no PTY, no shell). This avoids interactive-prompt
// detection entirely and is the correct approach for devices like MikroTik
// RouterOS that send ANSI terminal-capability queries before the prompt.
//
// Each call to Session.Run opens a fresh SSH channel, executes the command,
// and returns its combined stdout. The returned closer tears down the
// underlying SSH client.
func DialSSHExec(ctx context.Context, cfg SSHConfig) (drivers.Session, func() error, error) {
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

	hk := resolveHostKeyCallback(ctx, cfg)
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
	s := &sshExecSession{client: client, timeout: cfg.Timeout}
	return s, client.Close, nil
}

// sshExecSession implements drivers.Session by opening a fresh SSH exec
// channel for every Run call. No interactive shell or PTY is involved.
type sshExecSession struct {
	client  *ssh.Client
	timeout time.Duration
}

func (s *sshExecSession) Run(ctx context.Context, cmd string) (string, error) {
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, s.timeout)
		defer cancel()
	}

	sess, err := s.client.NewSession()
	if err != nil {
		return "", fmt.Errorf("transport/ssh: exec session: %w", err)
	}
	defer sess.Close()

	out, err := sess.Output(cmd)
	if err != nil {
		// ExitError just means the command returned non-zero; still return output.
		if _, ok := err.(*ssh.ExitError); ok {
			return string(out), nil
		}
		return "", fmt.Errorf("transport/ssh: exec %q: %w", cmd, err)
	}
	return string(out), nil
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
	hk := resolveHostKeyCallback(ctx, cfg)
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

// promptRE matches typical CLI prompts at end-of-output. It handles:
//   - Standard:  hostname#  hostname>  hostname$  hostname(config)#
//   - MikroTik:  [admin@MikroTik] >  [admin@MikroTik] /ip>
//
// Horizontal-only whitespace ([ 	]) is used in path segments so the pattern
// never spans newlines and cannot accidentally absorb preceding output lines.
var promptRE = regexp.MustCompile(`(?m)^(\[\S+\]|[\w.-]+)(\([^)]+\))?([ 	]+[/\w.-]+)*[ 	]*[#>$][ 	]*$`)

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

// resolveHostKeyCallback returns the ssh.HostKeyCallback to use for a dial.
// Priority:
//  1. cfg.HostKeyCallback (explicit override, e.g. tests)
//  2. cfg.KnownHosts (persistent DB-backed store — preferred for production)
//  3. Temporary MemKnownHostsStore (TOFU, keys forgotten on process exit)
func resolveHostKeyCallback(ctx context.Context, cfg SSHConfig) ssh.HostKeyCallback {
	if cfg.HostKeyCallback != nil {
		return cfg.HostKeyCallback
	}
	store := cfg.KnownHosts
	if store == nil {
		store = NewMemKnownHostsStore()
	}
	// Determine the authoritative port for key pinning.
	// If cfg.Address already contains an explicit port (e.g. "10.0.0.1:830")
	// use that; otherwise fall back to cfg.Port or the SSH default (22).
	port := cfg.Port
	if _, portStr, err := net.SplitHostPort(cfg.Address); err == nil {
		if p, perr := net.LookupPort("tcp", portStr); perr == nil && p > 0 {
			port = p
		}
	}
	if port == 0 {
		port = 22
	}
	return knownHostsCallback(ctx, cfg.TenantID, port, store)
}
