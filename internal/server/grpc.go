package server

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/i4Edu/netmantle/internal/poller"
	pollerv1 "github.com/i4Edu/netmantle/internal/poller/pollerv1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// GRPCConfig controls the poller gRPC listener shell.
type GRPCConfig struct {
	Address         string
	TLSCertFile     string
	TLSKeyFile      string
	TLSClientCAFile string
}

// GRPCServer hosts the poller wire listener with mandatory mTLS.
type GRPCServer struct {
	cfg    GRPCConfig
	wire   *poller.WireService
	log    *slog.Logger
	server *grpc.Server
	lis    net.Listener
}

// NewGRPCServer builds a gRPC listener shell configured for strict mTLS.
func NewGRPCServer(cfg GRPCConfig, wire *poller.WireService, log *slog.Logger) (*GRPCServer, error) {
	if cfg.Address == "" {
		return nil, errors.New("grpc: address required")
	}
	tlsCfg, err := loadMTLSConfig(cfg)
	if err != nil {
		return nil, err
	}
	lis, err := net.Listen("tcp", cfg.Address)
	if err != nil {
		return nil, fmt.Errorf("grpc: listen: %w", err)
	}
	s := grpc.NewServer(
		grpc.Creds(credentials.NewTLS(tlsCfg)),
	)
	pollerv1.RegisterPollerServiceServer(s, &pollerRPCServer{wire: wire, log: log})
	if log == nil {
		log = slog.Default()
	}
	return &GRPCServer{
		cfg:    cfg,
		wire:   wire,
		log:    log,
		server: s,
		lis:    lis,
	}, nil
}

func loadMTLSConfig(cfg GRPCConfig) (*tls.Config, error) {
	if cfg.TLSCertFile == "" || cfg.TLSKeyFile == "" || cfg.TLSClientCAFile == "" {
		return nil, errors.New("grpc: tls_cert_file, tls_key_file and tls_client_ca_file are required")
	}
	cert, err := tls.LoadX509KeyPair(cfg.TLSCertFile, cfg.TLSKeyFile)
	if err != nil {
		return nil, fmt.Errorf("grpc: load server certificate: %w", err)
	}
	caPEM, err := os.ReadFile(cfg.TLSClientCAFile)
	if err != nil {
		return nil, fmt.Errorf("grpc: read client ca file: %w", err)
	}
	pool := x509.NewCertPool()
	if ok := pool.AppendCertsFromPEM(caPEM); !ok {
		return nil, errors.New("grpc: parse client ca file")
	}
	return &tls.Config{
		MinVersion:   tls.VersionTLS13,
		Certificates: []tls.Certificate{cert},
		ClientCAs:    pool,
		ClientAuth:   tls.RequireAndVerifyClientCert,
	}, nil
}

// Start serves the gRPC listener until ctx is cancelled.
func (s *GRPCServer) Start(ctx context.Context) error {
	errCh := make(chan error, 1)
	go func() {
		s.log.Info("poller grpc listening", "addr", s.cfg.Address)
		err := s.server.Serve(s.lis)
		if err != nil && !errors.Is(err, grpc.ErrServerStopped) {
			errCh <- err
			return
		}
		errCh <- nil
	}()
	select {
	case <-ctx.Done():
		s.Shutdown(5 * time.Second)
		return nil
	case err := <-errCh:
		return err
	}
}

// Shutdown gracefully stops the gRPC server.
func (s *GRPCServer) Shutdown(timeout time.Duration) {
	done := make(chan struct{})
	go func() {
		s.server.GracefulStop()
		close(done)
	}()
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	select {
	case <-done:
	case <-time.After(timeout):
		s.server.Stop()
	}
}

type pollerRPCServer struct {
	pollerv1.UnimplementedPollerServiceServer
	wire *poller.WireService
	log  *slog.Logger

	mu       sync.Mutex
	sessions map[int64]pollerSession
	nonces   map[string]time.Time
	nonceGC  time.Time
}

type pollerSession struct {
	TenantID int64
	PollerID int64
	Token    string
	Expires  time.Time
}

func (s *pollerRPCServer) Authenticate(ctx context.Context, req *pollerv1.AuthenticateRequest) (*pollerv1.AuthenticateResponse, error) {
	if req.GetTenantId() <= 0 || strings.TrimSpace(req.GetPollerName()) == "" {
		return nil, status.Error(codes.InvalidArgument, "tenant_id and poller_name are required")
	}
	if req.GetClientTime() == nil {
		return nil, status.Error(codes.InvalidArgument, "client_time is required")
	}
	if len([]byte(strings.TrimSpace(req.GetNonce()))) < 16 {
		return nil, status.Error(codes.InvalidArgument, "nonce must be at least 16 bytes")
	}
	clientTime := req.GetClientTime().AsTime()
	now := time.Now().UTC()
	skew := now.Sub(clientTime)
	if skew < 0 {
		skew = -skew
	}
	if skew > 60*time.Second {
		return nil, status.Error(codes.Unauthenticated, "stale authenticate timestamp")
	}
	token := bearerTokenFromMetadata(ctx)
	if token == "" {
		return nil, status.Error(codes.Unauthenticated, "missing bearer token")
	}
	if !s.tryUseNonce(req.GetTenantId(), req.GetPollerName(), req.GetNonce(), now) {
		return nil, status.Error(codes.Unauthenticated, "replayed nonce")
	}
	p, refresh, err := s.wire.Authenticate(ctx, req.GetTenantId(), req.GetPollerName(), token)
	if err != nil {
		return nil, status.Error(codes.Unauthenticated, err.Error())
	}
	s.recordSession(p.TenantID, p.ID, token, refresh)
	return &pollerv1.AuthenticateResponse{
		Poller: &pollerv1.Poller{
			Id:       p.ID,
			TenantId: p.TenantID,
			Zone:     p.Zone,
			Name:     p.Name,
			LastSeen: timestamppb.New(p.LastSeen),
		},
		RefreshBefore: timestamppb.New(refresh),
	}, nil
}

func (s *pollerRPCServer) ClaimJob(ctx context.Context, req *pollerv1.ClaimJobRequest) (*pollerv1.ClaimJobResponse, error) {
	if req.GetTenantId() <= 0 || req.GetPollerId() <= 0 {
		return nil, status.Error(codes.InvalidArgument, "tenant_id and poller_id are required")
	}
	if err := s.requireSession(ctx, req.GetTenantId(), req.GetPollerId()); err != nil {
		return nil, err
	}
	job, err := s.wire.Claim(ctx, req.GetTenantId(), req.GetPollerId(), poller.ParseJobTypes(req.GetSupportedJobTypes()))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, status.Error(codes.NotFound, "no matching queued job")
		}
		return nil, status.Error(codes.Internal, err.Error())
	}
	var expiresAt *timestamppb.Timestamp
	if job.ExpiresAt != nil {
		expiresAt = timestamppb.New(*job.ExpiresAt)
	}
	var deadline *durationpb.Duration
	if job.ExpiresAt != nil && job.ClaimedAt != nil {
		deadline = durationpb.New(job.ExpiresAt.Sub(*job.ClaimedAt))
	}
	return &pollerv1.ClaimJobResponse{
		Job: &pollerv1.Job{
			Id:             job.ID,
			TenantId:       job.TenantID,
			IdempotencyKey: job.IdempotencyKey,
			DeviceId:       job.DeviceID,
			JobType:        string(job.JobType),
			PayloadJson:    job.Payload,
			CreatedAt:      timestamppb.New(job.CreatedAt),
			ExpiresAt:      expiresAt,
			Deadline:       deadline,
		},
	}, nil
}

func (s *pollerRPCServer) ReportResult(ctx context.Context, req *pollerv1.ReportResultRequest) (*pollerv1.ReportResultResponse, error) {
	if req.GetTenantId() <= 0 || req.GetPollerId() <= 0 || req.GetJobId() <= 0 {
		return nil, status.Error(codes.InvalidArgument, "tenant_id, poller_id and job_id are required")
	}
	if err := s.requireSession(ctx, req.GetTenantId(), req.GetPollerId()); err != nil {
		return nil, err
	}
	if err := s.wire.ReportResult(ctx, req.GetTenantId(), req.GetPollerId(), req.GetJobId(), req.GetSuccess(), req.GetResultJson(), req.GetError()); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, status.Error(codes.PermissionDenied, "job is not currently owned by poller")
		}
		return nil, status.Error(codes.Internal, err.Error())
	}
	return &pollerv1.ReportResultResponse{Accepted: true}, nil
}

func (s *pollerRPCServer) StreamJobs(req *pollerv1.StreamJobsRequest, stream grpc.ServerStreamingServer[pollerv1.JobAvailable]) error {
	if req.GetTenantId() <= 0 || req.GetPollerId() <= 0 {
		return status.Error(codes.InvalidArgument, "tenant_id and poller_id are required")
	}
	if err := s.requireSession(stream.Context(), req.GetTenantId(), req.GetPollerId()); err != nil {
		return err
	}
	jobType := "backup"
	if types := req.GetSupportedJobTypes(); len(types) > 0 && strings.TrimSpace(types[0]) != "" {
		jobType = types[0]
	}
	// Lightweight keep-alive hint stream: clients should still call ClaimJob.
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
	for {
		if err := stream.Send(&pollerv1.JobAvailable{JobType: jobType}); err != nil {
			return err
		}
		select {
		case <-stream.Context().Done():
			return nil
		case <-ticker.C:
		}
	}
}

func (s *pollerRPCServer) Health(ctx context.Context, req *pollerv1.HealthRequest) (*pollerv1.HealthResponse, error) {
	if req.GetTenantId() <= 0 || req.GetPollerId() <= 0 {
		return nil, status.Error(codes.InvalidArgument, "tenant_id and poller_id are required")
	}
	if err := s.requireSession(ctx, req.GetTenantId(), req.GetPollerId()); err != nil {
		return nil, err
	}
	return &pollerv1.HealthResponse{
		Healthy:              true,
		StatusMessage:        "poller wire listener active",
		CoreObservedLastSeen: timestamppb.Now(),
	}, nil
}

func bearerTokenFromMetadata(ctx context.Context) string {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return ""
	}
	auths := md.Get("authorization")
	if len(auths) == 0 {
		return ""
	}
	h := strings.TrimSpace(auths[0])
	const prefix = "Bearer "
	if !strings.HasPrefix(h, prefix) {
		return ""
	}
	return strings.TrimSpace(h[len(prefix):])
}

func (s *pollerRPCServer) recordSession(tenantID, pollerID int64, token string, expires time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.sessions == nil {
		s.sessions = make(map[int64]pollerSession)
	}
	s.sessions[pollerID] = pollerSession{
		TenantID: tenantID,
		PollerID: pollerID,
		Token:    token,
		Expires:  expires,
	}
}

func (s *pollerRPCServer) requireSession(ctx context.Context, tenantID, pollerID int64) error {
	token := bearerTokenFromMetadata(ctx)
	if token == "" {
		return status.Error(codes.Unauthenticated, "missing bearer token")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.purgeExpiredSessionsLocked(time.Now().UTC())
	session, ok := s.sessions[pollerID]
	if !ok {
		return status.Error(codes.Unauthenticated, "no active poller session; call Authenticate")
	}
	if session.TenantID != tenantID {
		return status.Error(codes.PermissionDenied, "poller is not registered for tenant")
	}
	if session.Token != token {
		return status.Error(codes.Unauthenticated, "bearer token does not match poller session")
	}
	return nil
}

func (s *pollerRPCServer) purgeExpiredSessionsLocked(now time.Time) {
	if s.sessions == nil {
		return
	}
	for pollerID, session := range s.sessions {
		if !session.Expires.IsZero() && now.After(session.Expires) {
			delete(s.sessions, pollerID)
		}
	}
}

func (s *pollerRPCServer) tryUseNonce(tenantID int64, pollerName, nonce string, now time.Time) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.nonces == nil {
		s.nonces = make(map[string]time.Time)
	}
	ttl := 60 * time.Second
	if s.nonceGC.IsZero() || now.After(s.nonceGC) {
		for key, exp := range s.nonces {
			if now.After(exp) {
				delete(s.nonces, key)
			}
		}
		s.nonceGC = now.Add(30 * time.Second)
	}
	key := nonceCacheKey(tenantID, pollerName, nonce)
	if exp, ok := s.nonces[key]; ok && now.Before(exp) {
		return false
	}
	s.nonces[key] = now.Add(ttl)
	return true
}

func nonceCacheKey(tenantID int64, pollerName, nonce string) string {
	sum := sha256.Sum256([]byte(fmt.Sprintf("%d\x00%s\x00%s", tenantID, pollerName, nonce)))
	return hex.EncodeToString(sum[:])
}
