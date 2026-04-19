package gitops

import (
	"context"
	"testing"
	"time"

	"github.com/i4Edu/netmantle/internal/configstore"
	"github.com/i4Edu/netmantle/internal/crypto"
	"github.com/i4Edu/netmantle/internal/storage"
)

func TestConfigureRoundTrip(t *testing.T) {
	db, _ := storage.Open("sqlite", ":memory:")
	defer db.Close()
	if err := storage.Migrate(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	res, _ := db.Exec(`INSERT INTO tenants(name, created_at) VALUES('t', ?)`, time.Now().Format(time.RFC3339))
	tid, _ := res.LastInsertId()

	store, _ := configstore.New(t.TempDir())
	sealer, _ := crypto.NewSealer("kek")
	svc := New(db, store, sealer)

	if err := svc.Configure(context.Background(), tid, "https://example/git/repo.git", "main", "supersecret"); err != nil {
		t.Fatal(err)
	}
	got, err := svc.Get(context.Background(), tid)
	if err != nil || got == nil || got.RemoteURL == "" {
		t.Fatalf("get: %v %+v", err, got)
	}
	// Token must not be exposed by Get; Verify by reading the row directly
	// and unsealing, which should match the original.
	var env string
	_ = db.QueryRow(`SELECT secret_envelope FROM gitops_mirrors WHERE tenant_id=?`, tid).Scan(&env)
	pt, err := sealer.Open(env)
	if err != nil {
		t.Fatal(err)
	}
	if string(pt) != "supersecret" {
		t.Fatalf("decrypted: %q", pt)
	}

	// PushDevice with no commits returns an error from go-git (open fails);
	// it should not panic.
	if err := svc.PushDevice(context.Background(), tid, 999); err == nil {
		t.Logf("pushdevice returned nil (no repo); acceptable")
	}
}
