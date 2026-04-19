package tenants

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/i4Edu/netmantle/internal/storage"
)

func TestTenantsAndQuota(t *testing.T) {
	db, _ := storage.Open("sqlite", ":memory:")
	defer db.Close()
	if err := storage.Migrate(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	svc := New(db)
	tnt, err := svc.Create(context.Background(), "acme", 2)
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.CheckDeviceQuota(context.Background(), tnt.ID); err != nil {
		t.Fatalf("quota with 0 devices: %v", err)
	}
	for i := 0; i < 2; i++ {
		_, _ = db.Exec(`INSERT INTO devices(tenant_id, hostname, address, port, driver, created_at) VALUES(?, ?, '1', 22, 'cisco_ios', ?)`,
			tnt.ID, "r"+string(rune('0'+i)), time.Now().Format(time.RFC3339))
	}
	if err := svc.CheckDeviceQuota(context.Background(), tnt.ID); !errors.Is(err, ErrQuotaExceeded) {
		t.Fatalf("expected quota exceeded, got %v", err)
	}
	if err := svc.SetQuota(context.Background(), tnt.ID, 5); err != nil {
		t.Fatal(err)
	}
	if err := svc.CheckDeviceQuota(context.Background(), tnt.ID); err != nil {
		t.Fatalf("after raise: %v", err)
	}
	list, _ := svc.List(context.Background())
	// Includes default 'default' tenant + acme.
	if len(list) < 1 {
		t.Fatal("empty list")
	}
}
