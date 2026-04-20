// Package transport — Telnet transport adapter.
//
// DialTelnet returns a drivers.Session that communicates over a raw TCP
// connection using the Telnet protocol (RFC 854). It handles:
//   - IAC option negotiation (WILL/WONT/DO/DONT) — all options are refused
//     so the server falls back to a plain 7-bit dumb terminal.
//   - Login/password prompts (detected by suffix matching).
//   - CLI prompts (reuses the same promptRE from ssh.go).
//
// This transport is suitable for legacy devices that do not support SSH.
// Use SSH where possible — Telnet sends credentials in cleartext.
package transport

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/i4Edu/netmantle/internal/drivers"
)

// TelnetConfig holds connection parameters for a Telnet session.
type TelnetConfig struct {
	Address  string // host or host:port
	Port     int    // used when Address has no port; default 23
	Username string
	Password string
	Timeout  time.Duration // default 30 s
}

// DialTelnet opens a Telnet session, negotiates away all options, handles the
// login/password exchange, and returns a drivers.Session ready to accept
// commands. The returned closer tears down the underlying TCP connection.
func DialTelnet(ctx context.Context, cfg TelnetConfig) (drivers.Session, func() error, error) {
	if cfg.Timeout == 0 {
		cfg.Timeout = 30 * time.Second
	}
	addr := cfg.Address
	if _, _, err := net.SplitHostPort(addr); err != nil {
		port := cfg.Port
		if port == 0 {
			port = 23
		}
		addr = net.JoinHostPort(addr, strconv.Itoa(port))
	}

	d := net.Dialer{Timeout: cfg.Timeout}
	conn, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, nil, fmt.Errorf("transport/telnet: dial %s: %w", addr, err)
	}

	s := &telnetSession{
		conn:    conn,
		timeout: cfg.Timeout,
	}

	// Perform the Telnet handshake + login.
	if err := s.login(ctx, cfg.Username, cfg.Password); err != nil {
		_ = conn.Close()
		return nil, nil, fmt.Errorf("transport/telnet: login: %w", err)
	}

	return s, conn.Close, nil
}

// telnetSession implements drivers.Session over a raw Telnet TCP connection.
type telnetSession struct {
	conn    net.Conn
	timeout time.Duration
}

// Telnet IAC bytes (RFC 854).
const (
	iacByte = 0xFF
	iacDont = 0xFE
	iacDo   = 0xFD
	iacWont = 0xFC
	iacWill = 0xFB
	iacSB   = 0xFA // subnegotiation begin
	iacSE   = 0xF0 // subnegotiation end
)

// login reads the initial banner, responds to IAC option negotiations,
// sends username/password when prompted, then waits for the CLI prompt.
func (s *telnetSession) login(ctx context.Context, user, pass string) error {
	deadline := time.Now().Add(s.timeout)
	s.conn.SetDeadline(deadline) //nolint:errcheck

	// Read until we see a Username/Login prompt (case-insensitive) or a CLI prompt.
	banner, err := s.readUntilLoginOrPrompt(ctx)
	if err != nil {
		return fmt.Errorf("waiting for login prompt: %w", err)
	}

	// If we already see a CLI prompt (no auth required) we are done.
	if promptRE.MatchString(banner) {
		return nil
	}

	// Send username.
	if _, err := fmt.Fprintf(s.conn, "%s\r\n", user); err != nil {
		return fmt.Errorf("send username: %w", err)
	}

	// Read until password prompt.
	_, err = s.readUntilSuffix(ctx, "Password:", "password:", "assword", "ASSWORD")
	if err != nil {
		// Some devices skip the password prompt (key-auth or no-auth).
		if errors.Is(err, errPromptSeen) {
			return nil
		}
		return fmt.Errorf("waiting for password prompt: %w", err)
	}

	// Send password.
	if _, err := fmt.Fprintf(s.conn, "%s\r\n", pass); err != nil {
		return fmt.Errorf("send password: %w", err)
	}

	// Drain until we see the CLI prompt.
	_, err = s.readUntilPrompt(ctx)
	return err
}

var errPromptSeen = errors.New("prompt seen")

// readUntilLoginOrPrompt reads bytes, strips IAC sequences, and returns when
// it sees a login prompt or a CLI prompt.
func (s *telnetSession) readUntilLoginOrPrompt(ctx context.Context) (string, error) {
	var buf bytes.Buffer
	chunk := make([]byte, 512)
	deadline := time.Now().Add(s.timeout)

	for {
		if ctx.Err() != nil {
			return buf.String(), ctx.Err()
		}
		s.conn.SetReadDeadline(deadline) //nolint:errcheck
		n, err := s.conn.Read(chunk)
		if n > 0 {
			stripped := s.stripIAC(chunk[:n])
			buf.Write(stripped)
		}
		b := buf.String()
		lower := strings.ToLower(b)
		if strings.HasSuffix(strings.TrimRight(lower, " \t\r\n"), "login:") ||
			strings.HasSuffix(strings.TrimRight(lower, " \t\r\n"), "username:") ||
			strings.Contains(lower, "username:") || strings.Contains(lower, "login:") {
			return b, nil
		}
		if promptRE.MatchString(b) {
			return b, nil
		}
		if err != nil {
			if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
				return b, fmt.Errorf("transport/telnet: timeout waiting for login prompt")
			}
			if err == io.EOF {
				return b, nil
			}
			return b, err
		}
	}
}

// readUntilSuffix reads until output ends with one of the given suffixes.
// Returns errPromptSeen if a CLI prompt is detected first.
func (s *telnetSession) readUntilSuffix(ctx context.Context, suffixes ...string) (string, error) {
	var buf bytes.Buffer
	chunk := make([]byte, 512)
	deadline := time.Now().Add(s.timeout)

	for {
		if ctx.Err() != nil {
			return buf.String(), ctx.Err()
		}
		s.conn.SetReadDeadline(deadline) //nolint:errcheck
		n, err := s.conn.Read(chunk)
		if n > 0 {
			stripped := s.stripIAC(chunk[:n])
			buf.Write(stripped)
		}
		b := buf.String()
		for _, suf := range suffixes {
			if strings.Contains(b, suf) {
				return b, nil
			}
		}
		if promptRE.MatchString(b) {
			return b, errPromptSeen
		}
		if err != nil {
			if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
				return b, fmt.Errorf("transport/telnet: suffix timeout")
			}
			if err == io.EOF {
				return b, nil
			}
			return b, err
		}
	}
}

// Run implements drivers.Session. It sends cmd followed by CR+LF and reads
// until the CLI prompt appears, then strips the echoed command and prompt.
func (s *telnetSession) Run(ctx context.Context, cmd string) (string, error) {
	deadline, hasDeadline := ctx.Deadline()
	if !hasDeadline {
		deadline = time.Now().Add(s.timeout)
	}
	s.conn.SetDeadline(deadline) //nolint:errcheck

	if _, err := fmt.Fprintf(s.conn, "%s\r\n", cmd); err != nil {
		return "", fmt.Errorf("transport/telnet: write: %w", err)
	}
	out, err := s.readUntilPrompt(ctx)
	if err != nil {
		return "", err
	}
	return cleanCommandEcho(out, cmd), nil
}

// readUntilPrompt reads stripped bytes until promptRE matches the tail.
func (s *telnetSession) readUntilPrompt(ctx context.Context) (string, error) {
	deadline, hasDeadline := ctx.Deadline()
	if !hasDeadline {
		deadline = time.Now().Add(s.timeout)
	}

	var buf bytes.Buffer
	chunk := make([]byte, 4096)
	for {
		if ctx.Err() != nil {
			return buf.String(), ctx.Err()
		}
		if time.Now().After(deadline) {
			return buf.String(), errors.New("transport/telnet: prompt timeout")
		}

		readCh := make(chan readResult, 1)
		go func() {
			n, err := s.conn.Read(chunk)
			readCh <- readResult{n: n, err: err, data: append([]byte(nil), chunk[:n]...)}
		}()

		select {
		case <-ctx.Done():
			return buf.String(), ctx.Err()
		case <-time.After(time.Until(deadline)):
			return buf.String(), errors.New("transport/telnet: prompt timeout")
		case r := <-readCh:
			if r.n > 0 {
				stripped := s.stripIAC(r.data[:r.n])
				buf.Write(stripped)
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

// stripIAC removes Telnet IAC option sequences from raw bytes and sends
// DONT responses so the server stops offering options we don't want.
//
// IAC sequences: IAC WILL/WONT/DO/DONT <option> (3 bytes each)
//
//	IAC SB ... IAC SE (subnegotiation, variable length)
//
// We reply to WILL with DONT, and to DO with WONT, coercing the server into
// NVT (Network Virtual Terminal) mode which is a plain 8-bit byte stream.
func (s *telnetSession) stripIAC(data []byte) []byte {
	out := make([]byte, 0, len(data))
	i := 0
	for i < len(data) {
		b := data[i]
		if b != iacByte {
			out = append(out, b)
			i++
			continue
		}
		// We have an IAC byte.
		if i+1 >= len(data) {
			i++ // truncated IAC — skip
			continue
		}
		cmd := data[i+1]
		switch cmd {
		case iacWill: // server wants to enable option
			if i+2 < len(data) {
				opt := data[i+2]
				// Reply DONT to decline.
				_, _ = s.conn.Write([]byte{iacByte, iacDont, opt})
				i += 3
			} else {
				i += 2
			}
		case iacDo: // server wants us to enable option
			if i+2 < len(data) {
				opt := data[i+2]
				// Reply WONT to decline.
				_, _ = s.conn.Write([]byte{iacByte, iacWont, opt})
				i += 3
			} else {
				i += 2
			}
		case iacWont, iacDont: // server acknowledging our refusal — just skip
			if i+2 < len(data) {
				i += 3
			} else {
				i += 2
			}
		case iacSB: // subnegotiation — skip until IAC SE
			i += 2
			for i < len(data) {
				if data[i] == iacByte && i+1 < len(data) && data[i+1] == iacSE {
					i += 2
					break
				}
				i++
			}
		case iacByte: // IAC IAC = literal 0xFF
			out = append(out, 0xFF)
			i += 2
		default:
			i += 2 // unknown 2-byte command — skip
		}
	}
	return out
}
