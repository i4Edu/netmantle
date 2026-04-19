package poller_test

import (
	"context"
	"testing"
	"time"

	"github.com/i4Edu/netmantle/internal/poller"
	"github.com/i4Edu/netmantle/internal/storage"
)

func newWireHarness(t *testing.T) (*poller.WireService, *poller.Service, *poller.JobService, func()) {
	t.Helper()
	db, err := storage.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	if err := storage.Migrate(context.Background(), db); err != nil {
		db.Close()
		t.Fatal(err)
	}
	pollers := poller.New(db)
	jobs := poller.NewJobService(db)
	wire := poller.NewWireService(pollers, jobs)
	return wire, pollers, jobs, func() { _ = db.Close() }
}

func TestWireAuthenticateClaimAndReport(t *testing.T) {
	wire, pollers, jobs, done := newWireHarness(t)
	defer done()
	seedTenant(t, jobs, 1)
	seedDevice(t, jobs, 1, 10)

	p, token, err := pollers.Register(context.Background(), 1, "z1", "poller-a")
	if err != nil {
		t.Fatal(err)
	}
	ap, refresh, err := wire.Authenticate(context.Background(), 1, p.Name, token)
	if err != nil {
		t.Fatalf("Authenticate: %v", err)
	}
	if ap.ID != p.ID {
		t.Fatalf("expected poller id %d, got %d", p.ID, ap.ID)
	}
	if time.Until(refresh) <= 0 {
		t.Fatal("expected refresh deadline in the future")
	}

	const emptyPayloadJSON = "{}"
	enq, err := jobs.Enqueue(context.Background(), 1, 10, poller.JobTypeBackup, emptyPayloadJSON, "wire-flow", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	claimed, err := wire.Claim(context.Background(), 1, p.ID, poller.ParseJobTypes([]string{"backup"}))
	if err != nil {
		t.Fatalf("Claim: %v", err)
	}
	if claimed.ID != enq.ID {
		t.Fatalf("expected claimed id %d, got %d", enq.ID, claimed.ID)
	}
	if err := wire.ReportResult(context.Background(), 1, p.ID, claimed.ID, true, `{"sha":"abc"}`, ""); err != nil {
		t.Fatalf("ReportResult: %v", err)
	}
}

func TestParseJobTypesIgnoresUnknownValues(t *testing.T) {
	got := poller.ParseJobTypes([]string{"backup", "invalid", "probe"})
	if len(got) != 2 || got[0] != poller.JobTypeBackup || got[1] != poller.JobTypeProbe {
		t.Fatalf("unexpected ParseJobTypes result: %#v", got)
	}
}
