package poller

import (
	"context"
	"time"
)

// WireService is a transport-agnostic adapter for the poller wire protocol.
// It maps protocol RPC intents (authenticate/claim/report) to the persisted
// poller and queue services.
type WireService struct {
	Pollers pollerAuth
	Jobs    wireJobs

	// LeaseTTL controls refresh_before returned by Authenticate.
	LeaseTTL time.Duration
}

type pollerAuth interface {
	Authenticate(ctx context.Context, tenantID int64, pollerName, token string) (Poller, error)
}

type wireJobs interface {
	Claim(ctx context.Context, tenantID, pollerID int64, supportedTypes []JobType) (Job, error)
	CompleteClaimedBy(ctx context.Context, tenantID, pollerID, jobID int64, success bool, resultJSON, errMsg string) error
}

// NewWireService constructs a wire adapter with a conservative default lease.
func NewWireService(pollers pollerAuth, jobs wireJobs) *WireService {
	return &WireService{
		Pollers:  pollers,
		Jobs:     jobs,
		LeaseTTL: 2 * time.Minute,
	}
}

// Authenticate verifies the poller bootstrap token and returns a lease
// refresh timestamp suitable for wire-protocol responses.
func (w *WireService) Authenticate(ctx context.Context, tenantID int64, pollerName, token string) (Poller, time.Time, error) {
	p, err := w.Pollers.Authenticate(ctx, tenantID, pollerName, token)
	if err != nil {
		return Poller{}, time.Time{}, err
	}
	ttl := w.LeaseTTL
	if ttl <= 0 {
		ttl = 2 * time.Minute
	}
	return p, time.Now().UTC().Add(ttl), nil
}

// Claim atomically claims a tenant-scoped job for pollerID.
// Tenant ownership validation is delegated to JobService.Claim.
func (w *WireService) Claim(ctx context.Context, tenantID, pollerID int64, supportedTypes []JobType) (Job, error) {
	return w.Jobs.Claim(ctx, tenantID, pollerID, supportedTypes)
}

// ReportResult finalizes a claimed/running job only if pollerID is still the
// owner for the specified tenant.
func (w *WireService) ReportResult(ctx context.Context, tenantID, pollerID, jobID int64, success bool, resultJSON, errMsg string) error {
	return w.Jobs.CompleteClaimedBy(ctx, tenantID, pollerID, jobID, success, resultJSON, errMsg)
}

// ParseJobTypes converts wire-level job-type strings into internal enums.
// Unknown values are ignored; an empty result means "all supported types".
func ParseJobTypes(types []string) []JobType {
	if len(types) == 0 {
		return nil
	}
	out := make([]JobType, 0, len(types))
	for _, t := range types {
		switch JobType(t) {
		case JobTypeBackup, JobTypeProbe, JobTypeCustom:
			out = append(out, JobType(t))
		}
	}
	return out
}
