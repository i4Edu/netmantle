package devices

import (
	"context"
	"testing"

	"github.com/i4Edu/netmantle/internal/storage"
)

func setup(t *testing.T) (*Repo, int64) {
	t.Helper()
	db, err := storage.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err := storage.Migrate(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	res, err := db.Exec(`INSERT INTO tenants(name, created_at) VALUES('t', '2026-01-01T00:00:00Z')`)
	if err != nil {
		t.Fatal(err)
	}
	id, _ := res.LastInsertId()
	return NewRepo(db), id
}

func TestDeviceCRUD(t *testing.T) {
	r, tid := setup(t)
	ctx := context.Background()
	d, err := r.CreateDevice(ctx, Device{TenantID: tid, Hostname: "r1", Address: "10.0.0.1", Port: 22, Driver: "cisco_ios"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if d.ID == 0 {
		t.Fatal("no id")
	}
	got, err := r.GetDevice(ctx, tid, d.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Hostname != "r1" {
		t.Fatalf("hostname: %s", got.Hostname)
	}
	d.Hostname = "r1-renamed"
	upd, err := r.UpdateDevice(ctx, d)
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if upd.Hostname != "r1-renamed" {
		t.Fatal("update did not persist")
	}
	list, err := r.ListDevices(ctx, tid)
	if err != nil || len(list) != 1 {
		t.Fatalf("list: %v %d", err, len(list))
	}
	if err := r.DeleteDevice(ctx, tid, d.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := r.GetDevice(ctx, tid, d.ID); err != ErrNotFound {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestValidateDevice(t *testing.T) {
	r, tid := setup(t)
	ctx := context.Background()
	cases := []Device{
		{TenantID: 0, Hostname: "x", Address: "1", Port: 22, Driver: "d"},
		{TenantID: tid, Hostname: "", Address: "1", Port: 22, Driver: "d"},
		{TenantID: tid, Hostname: "x", Address: "", Port: 22, Driver: "d"},
		{TenantID: tid, Hostname: "x", Address: "1", Port: 0, Driver: "d"},
		{TenantID: tid, Hostname: "x", Address: "1", Port: 22, Driver: ""},
	}
	for i, c := range cases {
		if _, err := r.CreateDevice(ctx, c); err == nil {
			t.Errorf("case %d: expected error", i)
		}
	}
}

func TestGroups(t *testing.T) {
	r, tid := setup(t)
	ctx := context.Background()
	g, err := r.CreateGroup(ctx, Group{TenantID: tid, Name: "core"})
	if err != nil {
		t.Fatal(err)
	}
	if g.ID == 0 {
		t.Fatal("no id")
	}
	gs, err := r.ListGroups(ctx, tid)
	if err != nil || len(gs) != 1 {
		t.Fatalf("list: %v %d", err, len(gs))
	}
}
