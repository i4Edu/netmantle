package audit

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/i4Edu/netmantle/internal/storage"
)

func newTestDB(t *testing.T) *Service {
	t.Helper()
	db, err := storage.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err := storage.Migrate(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	return New(db, slog.New(slog.NewTextHandler(io.Discard, nil)))
}

func TestRecordAndList(t *testing.T) {
	s := newTestDB(t)
	ctx := context.Background()

	s.Record(ctx, 1, 42, SourceAPI, "device.create", "device:7", "hostname=core-1")
	s.Record(ctx, 1, 42, SourceUI, "device.delete", "device:7", "")
	s.Record(ctx, 1, 99, SourceAPI, "credential.create", "credential:3", "name=lab")
	// Tenant 2 — must not leak to tenant 1.
	s.Record(ctx, 2, 1, SourceAPI, "device.create", "device:1", "")

	got, err := s.List(ctx, ListFilter{TenantID: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("want 3 rows for tenant 1, got %d", len(got))
	}
	// Newest first.
	if got[0].Action != "credential.create" {
		t.Fatalf("want newest = credential.create, got %q", got[0].Action)
	}
	if got[0].ActorUserID == nil || *got[0].ActorUserID != 99 {
		t.Fatalf("actor not recorded: %+v", got[0].ActorUserID)
	}
	if got[0].Source != SourceAPI {
		t.Fatalf("source = %q", got[0].Source)
	}
	if got[0].CreatedAt.IsZero() {
		t.Fatalf("CreatedAt zero")
	}
}

func TestListFilters(t *testing.T) {
	s := newTestDB(t)
	ctx := context.Background()

	s.Record(ctx, 1, 1, SourceAPI, "device.create", "device:1", "")
	s.Record(ctx, 1, 2, SourceUI, "device.create", "device:2", "")
	s.Record(ctx, 1, 1, SourceAPI, "device.delete", "device:1", "")

	// Filter by actor.
	got, _ := s.List(ctx, ListFilter{TenantID: 1, ActorUserID: 1})
	if len(got) != 2 {
		t.Fatalf("actor filter: want 2, got %d", len(got))
	}
	// Filter by action.
	got, _ = s.List(ctx, ListFilter{TenantID: 1, Action: "device.create"})
	if len(got) != 2 {
		t.Fatalf("action filter: want 2, got %d", len(got))
	}
	// Filter by target substring.
	got, _ = s.List(ctx, ListFilter{TenantID: 1, Target: "device:2"})
	if len(got) != 1 || got[0].Target != "device:2" {
		t.Fatalf("target filter: want device:2, got %+v", got)
	}
	// Limit.
	got, _ = s.List(ctx, ListFilter{TenantID: 1, Limit: 2})
	if len(got) != 2 {
		t.Fatalf("limit: want 2, got %d", len(got))
	}
}

func TestListSinceUntil(t *testing.T) {
	s := newTestDB(t)
	ctx := context.Background()

	s.Record(ctx, 1, 1, SourceAPI, "a", "", "")
	mid := time.Now().UTC()
	time.Sleep(5 * time.Millisecond)
	s.Record(ctx, 1, 1, SourceAPI, "b", "", "")

	got, err := s.List(ctx, ListFilter{TenantID: 1, Since: mid})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Action != "b" {
		t.Fatalf("since: want only b, got %+v", got)
	}
}

func TestRecordZeroIDsStoreNull(t *testing.T) {
	s := newTestDB(t)
	ctx := context.Background()

	s.Record(ctx, 0, 0, SourceSystem, "system.startup", "", "")
	got, _ := s.List(ctx, ListFilter{Limit: 1})
	if len(got) != 1 {
		t.Fatalf("want 1 row, got %d", len(got))
	}
	if got[0].TenantID != nil || got[0].ActorUserID != nil {
		t.Fatalf("expected NULL tenant/actor, got %+v / %+v",
			got[0].TenantID, got[0].ActorUserID)
	}
}
