package discovery

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/i4Edu/netmantle/internal/storage"
)

type fakeDialer struct {
	open map[string]bool
}

func (f *fakeDialer) DialContext(ctx context.Context, _ string, addr string) (net.Conn, error) {
	host, _, _ := net.SplitHostPort(addr)
	if !f.open[host] {
		return nil, &net.OpError{Op: "dial", Err: errClosed}
	}
	a, b := net.Pipe()
	go func() {
		_, _ = a.Write([]byte("SSH-2.0-Cisco-1.25\r\n"))
		time.Sleep(50 * time.Millisecond)
		a.Close()
	}()
	return b, nil
}

type errStr string

func (e errStr) Error() string { return string(e) }

var errClosed = errStr("closed")

func TestExpandCIDR(t *testing.T) {
	out, err := expandCIDR("10.0.0.0/30")
	if err != nil {
		t.Fatal(err)
	}
	// /30 has 4 addresses; we trim network + broadcast → 2 hosts.
	if len(out) != 2 {
		t.Fatalf("got %v", out)
	}
	if _, err := expandCIDR("10.0.0.0/8"); err == nil {
		t.Fatal("expected /8 rejection")
	}
}

func TestSweepFingerprintsCisco(t *testing.T) {
	db, err := storage.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := storage.Migrate(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	res, _ := db.Exec(`INSERT INTO tenants(name, created_at) VALUES('t', '2026-01-01T00:00:00Z')`)
	tid, _ := res.LastInsertId()

	svc := New(db)
	svc.Dialer = &fakeDialer{open: map[string]bool{"10.0.0.1": true}}
	_, results, err := svc.Run(context.Background(), tid, "10.0.0.0/30", 22, 8)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || results[0].Address != "10.0.0.1" {
		t.Fatalf("results: %+v", results)
	}
	if results[0].SuggestedDriver != "cisco_ios" {
		t.Fatalf("driver: %q", results[0].SuggestedDriver)
	}
}

func TestImportNetBox(t *testing.T) {
	body := []byte(`{"results":[
		{"name":"r1","primary_ip":{"address":"10.0.0.1/24"},"device_type":{"manufacturer":{"slug":"cisco"}}},
		{"name":"sw2","primary_ip":{"address":"10.0.0.2/24"},"device_type":{"manufacturer":{"slug":"arista"}}},
		{"name":"","primary_ip":{"address":"10.0.0.3/24"},"device_type":{"manufacturer":{"slug":"x"}}}
	]}`)
	items, err := ImportNetBox(body)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 {
		t.Fatalf("items: %+v", items)
	}
	if items[0].Driver != "cisco_ios" || items[0].Address != "10.0.0.1" {
		t.Fatalf("first: %+v", items[0])
	}
	if items[1].Driver != "arista_eos" {
		t.Fatalf("second: %+v", items[1])
	}
}
