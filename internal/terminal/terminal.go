// Package terminal implements the in-app web CLI (Phase 7).
//
// It bridges a browser WebSocket to an SSH "shell" session against a
// device, recording the full transcript to terminal_sessions.transcript
// for audit. Real PTY/SSH wiring lives in the transport layer; this file
// owns the WebSocket framing, transcript capture, and session metadata.
//
// We implement a minimal RFC 6455 server-side handshake + frame parser to
// avoid pulling a third-party WebSocket dependency. The protocol below is
// limited to text frames plus close/ping; it is sufficient for the CLI
// proxy and for our integration tests.
package terminal

import (
	"bufio"
	"context"
	"crypto/sha1"
	"database/sql"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

// SessionInfo is a row in terminal_sessions.
type SessionInfo struct {
	ID         int64     `json:"id"`
	TenantID   int64     `json:"tenant_id"`
	UserID     int64     `json:"user_id"`
	DeviceID   int64     `json:"device_id"`
	StartedAt  time.Time `json:"started_at"`
	EndedAt    time.Time `json:"ended_at,omitempty"`
	Transcript string    `json:"transcript,omitempty"`
}

// Backend is what the proxy talks to on the device side. In production a
// concrete implementation wraps an SSH "shell" channel; tests use a fake.
type Backend interface {
	io.ReadWriteCloser
}

// BackendFactory opens a Backend for one CLI session.
type BackendFactory func(ctx context.Context, tenantID, deviceID int64) (Backend, error)

// Service owns session persistence and the HTTP/WS handler.
type Service struct {
	DB      *sql.DB
	Factory BackendFactory
}

// New constructs a Service.
func New(db *sql.DB, f BackendFactory) *Service { return &Service{DB: db, Factory: f} }

// Handler returns an HTTP handler that performs the WebSocket upgrade and
// proxies traffic to the device backend. The caller is responsible for
// authn/authz and for parsing the device ID out of the request.
func (s *Service) Handler(deviceIDFn func(*http.Request) (tenantID, userID, deviceID int64, ok bool)) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tenantID, userID, deviceID, ok := deviceIDFn(r)
		if !ok {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		if !isWSUpgrade(r) {
			http.Error(w, "websocket upgrade required", http.StatusBadRequest)
			return
		}
		conn, brw, err := upgrade(w, r)
		if err != nil {
			return
		}
		defer conn.Close()

		// All writes to the hijacked conn must go through this single
		// mutex: the device→browser goroutine, the ping/close handler
		// in the request loop, and the connect-error path can otherwise
		// interleave bytes inside one frame.
		var writeMu sync.Mutex
		safeWrite := func(op byte, payload []byte) error {
			writeMu.Lock()
			defer writeMu.Unlock()
			return writeFrame(conn, op, payload)
		}

		backend, err := s.Factory(r.Context(), tenantID, deviceID)
		if err != nil {
			_ = safeWrite(opText, []byte("connect error: "+err.Error()))
			_ = safeWrite(opClose, nil)
			return
		}
		defer backend.Close()

		// Record session start.
		now := time.Now().UTC()
		res, err := s.DB.ExecContext(r.Context(),
			`INSERT INTO terminal_sessions(tenant_id, user_id, device_id, started_at) VALUES(?, ?, ?, ?)`,
			tenantID, userID, deviceID, now.Format(time.RFC3339))
		if err != nil {
			return
		}
		sessionID, _ := res.LastInsertId()

		var (
			transcriptMu sync.Mutex
			transcript   strings.Builder
		)
		recordOut := func(b []byte) {
			transcriptMu.Lock()
			defer transcriptMu.Unlock()
			transcript.Write(b)
		}

		// device → browser
		go func() {
			buf := make([]byte, 4096)
			for {
				n, err := backend.Read(buf)
				if n > 0 {
					recordOut(buf[:n])
					if err := safeWrite(opText, buf[:n]); err != nil {
						return
					}
				}
				if err != nil {
					_ = safeWrite(opClose, nil)
					return
				}
			}
		}()

		// browser → device
		for {
			op, payload, err := readFrame(brw)
			if err != nil {
				if errors.Is(err, errUnmaskedFrame) {
					// 1002 = protocol error. Best-effort close payload.
					_ = safeWrite(opClose, []byte{0x03, 0xea})
				}
				break
			}
			switch op {
			case opText, opBinary:
				recordOut(append([]byte("> "), payload...))
				if _, err := backend.Write(payload); err != nil {
					goto done
				}
			case opPing:
				_ = safeWrite(opPong, payload)
			case opClose:
				goto done
			}
		}
	done:
		_, _ = s.DB.ExecContext(r.Context(),
			`UPDATE terminal_sessions SET ended_at=?, transcript=? WHERE id=?`,
			time.Now().UTC().Format(time.RFC3339), transcript.String(), sessionID)
	})
}

// ------------------------------------------------------------------
// Minimal RFC 6455 server.
// ------------------------------------------------------------------

const (
	opText   byte = 0x1
	opBinary byte = 0x2
	opClose  byte = 0x8
	opPing   byte = 0x9
	opPong   byte = 0xa
)

func isWSUpgrade(r *http.Request) bool {
	return strings.EqualFold(r.Header.Get("Upgrade"), "websocket") &&
		strings.Contains(strings.ToLower(r.Header.Get("Connection")), "upgrade") &&
		r.Header.Get("Sec-WebSocket-Key") != ""
}

func upgrade(w http.ResponseWriter, r *http.Request) (io.ReadWriteCloser, *bufio.ReadWriter, error) {
	hj, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, "no hijacker", http.StatusInternalServerError)
		return nil, nil, errors.New("no hijacker")
	}
	conn, brw, err := hj.Hijack()
	if err != nil {
		return nil, nil, err
	}
	key := r.Header.Get("Sec-WebSocket-Key")
	accept := acceptKey(key)
	resp := "HTTP/1.1 101 Switching Protocols\r\n" +
		"Upgrade: websocket\r\nConnection: Upgrade\r\n" +
		"Sec-WebSocket-Accept: " + accept + "\r\n\r\n"
	if _, err := conn.Write([]byte(resp)); err != nil {
		conn.Close()
		return nil, nil, err
	}
	return conn, brw, nil
}

func acceptKey(key string) string {
	const guid = "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"
	h := sha1.Sum([]byte(key + guid))
	return base64.StdEncoding.EncodeToString(h[:])
}

// errUnmaskedFrame is returned when a client→server frame arrives without
// the masking bit set, which is a protocol error per RFC 6455 §5.1.
var errUnmaskedFrame = errors.New("terminal: client frame must be masked")

// readFrame reads a single client→server frame. RFC 6455 requires every
// client→server frame to be masked; unmasked frames are rejected.
func readFrame(br *bufio.ReadWriter) (op byte, payload []byte, err error) {
	hdr := make([]byte, 2)
	if _, err := io.ReadFull(br, hdr); err != nil {
		return 0, nil, err
	}
	op = hdr[0] & 0x0f
	masked := hdr[1]&0x80 != 0
	if !masked {
		return 0, nil, errUnmaskedFrame
	}
	plen := int(hdr[1] & 0x7f)
	switch plen {
	case 126:
		ext := make([]byte, 2)
		if _, err := io.ReadFull(br, ext); err != nil {
			return 0, nil, err
		}
		plen = int(binary.BigEndian.Uint16(ext))
	case 127:
		ext := make([]byte, 8)
		if _, err := io.ReadFull(br, ext); err != nil {
			return 0, nil, err
		}
		v := binary.BigEndian.Uint64(ext)
		if v > 1<<24 {
			return 0, nil, fmt.Errorf("frame too large")
		}
		plen = int(v)
	}
	var mask [4]byte
	if _, err := io.ReadFull(br, mask[:]); err != nil {
		return 0, nil, err
	}
	payload = make([]byte, plen)
	if _, err := io.ReadFull(br, payload); err != nil {
		return 0, nil, err
	}
	for i := range payload {
		payload[i] ^= mask[i%4]
	}
	return op, payload, nil
}

func writeFrame(w io.Writer, op byte, payload []byte) error {
	hdr := []byte{0x80 | op}
	switch {
	case len(payload) < 126:
		hdr = append(hdr, byte(len(payload)))
	case len(payload) <= 0xffff:
		hdr = append(hdr, 126, 0, 0)
		binary.BigEndian.PutUint16(hdr[len(hdr)-2:], uint16(len(payload)))
	default:
		hdr = append(hdr, 127, 0, 0, 0, 0, 0, 0, 0, 0)
		binary.BigEndian.PutUint64(hdr[len(hdr)-8:], uint64(len(payload)))
	}
	if _, err := w.Write(hdr); err != nil {
		return err
	}
	if len(payload) > 0 {
		if _, err := w.Write(payload); err != nil {
			return err
		}
	}
	if f, ok := w.(*bufio.ReadWriter); ok {
		return f.Flush()
	}
	return nil
}
