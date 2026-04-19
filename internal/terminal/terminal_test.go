package terminal

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/i4Edu/netmantle/internal/storage"
)

type fakeBackend struct {
	out    chan []byte
	in     chan []byte
	closed chan struct{}
}

func newFakeBackend() *fakeBackend {
	return &fakeBackend{
		out: make(chan []byte, 8), in: make(chan []byte, 8), closed: make(chan struct{}),
	}
}
func (f *fakeBackend) Read(p []byte) (int, error) {
	select {
	case b, ok := <-f.out:
		if !ok {
			return 0, io.EOF
		}
		n := copy(p, b)
		return n, nil
	case <-f.closed:
		return 0, io.EOF
	}
}
func (f *fakeBackend) Write(p []byte) (int, error) {
	cp := append([]byte{}, p...)
	select {
	case f.in <- cp:
	case <-f.closed:
		return 0, io.ErrClosedPipe
	}
	return len(p), nil
}
func (f *fakeBackend) Close() error { close(f.closed); return nil }

func TestTerminalProxy(t *testing.T) {
	db, _ := storage.Open("sqlite", ":memory:")
	defer db.Close()
	if err := storage.Migrate(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	be := newFakeBackend()
	svc := New(db, func(ctx context.Context, _ int64, _ int64) (Backend, error) {
		return be, nil
	})
	h := svc.Handler(func(_ *http.Request) (int64, int64, int64, bool) {
		return 1, 1, 42, true
	})
	srv := httptest.NewServer(h)
	defer srv.Close()

	// Manually do the WS handshake on a raw TCP connection.
	u := strings.TrimPrefix(srv.URL, "http://")
	conn, err := net.Dial("tcp", u)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	keyBytes := make([]byte, 16)
	_, _ = rand.Read(keyBytes)
	key := base64.StdEncoding.EncodeToString(keyBytes)
	req := "GET / HTTP/1.1\r\nHost: x\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Key: " + key + "\r\nSec-WebSocket-Version: 13\r\n\r\n"
	if _, err := conn.Write([]byte(req)); err != nil {
		t.Fatal(err)
	}
	br := bufio.NewReader(conn)
	for {
		line, err := br.ReadString('\n')
		if err != nil {
			t.Fatal(err)
		}
		if line == "\r\n" {
			break
		}
	}

	// Server sends 'hello'.
	be.out <- []byte("hello")
	op, payload, err := readFrameClient(br)
	if err != nil || op != opText || string(payload) != "hello" {
		t.Fatalf("recv: op=%x payload=%q err=%v", op, payload, err)
	}

	// Client sends 'show ver'.
	if err := writeMaskedFrame(conn, opText, []byte("show ver")); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-be.in:
		if string(got) != "show ver" {
			t.Fatalf("backend got: %q", got)
		}
	case <-time.After(time.Second):
		t.Fatal("backend never received client input")
	}

	// Close.
	_ = writeMaskedFrame(conn, opClose, nil)
	conn.Close()

	// Allow the handler goroutine to flush the transcript.
	for i := 0; i < 50; i++ {
		var transcript string
		_ = db.QueryRow(`SELECT IFNULL(transcript, '') FROM terminal_sessions WHERE id=1`).Scan(&transcript)
		if strings.Contains(transcript, "hello") && strings.Contains(transcript, "> show ver") {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("transcript never recorded")
}

// readFrameClient reads an unmasked server→client frame.
func readFrameClient(r io.Reader) (byte, []byte, error) {
	hdr := make([]byte, 2)
	if _, err := io.ReadFull(r, hdr); err != nil {
		return 0, nil, err
	}
	op := hdr[0] & 0x0f
	plen := int(hdr[1] & 0x7f)
	switch plen {
	case 126:
		ext := make([]byte, 2)
		if _, err := io.ReadFull(r, ext); err != nil {
			return 0, nil, err
		}
		plen = int(binary.BigEndian.Uint16(ext))
	case 127:
		ext := make([]byte, 8)
		if _, err := io.ReadFull(r, ext); err != nil {
			return 0, nil, err
		}
		plen = int(binary.BigEndian.Uint64(ext))
	}
	payload := make([]byte, plen)
	if _, err := io.ReadFull(r, payload); err != nil {
		return 0, nil, err
	}
	return op, payload, nil
}

func writeMaskedFrame(w io.Writer, op byte, payload []byte) error {
	hdr := []byte{0x80 | op}
	hdr = append(hdr, 0x80|byte(len(payload)))
	mask := []byte{1, 2, 3, 4}
	hdr = append(hdr, mask...)
	masked := make([]byte, len(payload))
	for i := range payload {
		masked[i] = payload[i] ^ mask[i%4]
	}
	if _, err := w.Write(hdr); err != nil {
		return err
	}
	if _, err := w.Write(masked); err != nil {
		return err
	}
	return nil
}
