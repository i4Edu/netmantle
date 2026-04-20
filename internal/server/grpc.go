package server

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"time"

	"github.com/i4Edu/netmantle/internal/poller"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/status"
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
		grpc.UnknownServiceHandler(func(_ any, stream grpc.ServerStream) error {
			_ = stream
			return status.Error(codes.Unimplemented, "poller gRPC wire service registration is pending")
		}),
	)
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
