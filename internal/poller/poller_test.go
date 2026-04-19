package poller

import (
	"context"
	"testing"
	"time"

	"github.com/i4Edu/netmantle/internal/storage"
)

func TestRegisterAndAuthenticate(t *testing.T) {
	db, _ := storage.Open("sqlite", ":memory:")
	defer db.Close()
	if err := storage.Migrate(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	res, _ := db.Exec(`INSERT INTO tenants(name, created_at) VALUES('t', ?)`, time.Now().Format(time.RFC3339))
	tid, _ := res.LastInsertId()

	svc := New(db)
	p, token, err := svc.Register(context.Background(), tid, "us-east-1", "poller-a")
	if err != nil {
		t.Fatal(err)
	}
	if p.ID == 0 || token == "" {
		t.Fatal("missing id or token")
	}

	got, err := svc.Authenticate(context.Background(), tid, "poller-a", token)
	if err != nil {
		t.Fatalf("auth: %v", err)
	}
	if got.LastSeen.IsZero() {
		t.Fatal("last_seen not set")
	}
	if _, err := svc.Authenticate(context.Background(), tid, "poller-a", "wrong"); err == nil {
		t.Fatal("expected auth failure")
	}

	list, err := svc.List(context.Background(), tid)
	if err != nil || len(list) != 1 {
		t.Fatalf("list: %v %d", err, len(list))
	}
}
