package server

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/i4Edu/netmantle/internal/poller"
	pollerv1 "github.com/i4Edu/netmantle/internal/poller/pollerv1"
	"github.com/i4Edu/netmantle/internal/storage"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/protobuf/types/known/emptypb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func TestLoadMTLSConfigRequiresFiles(t *testing.T) {
	_, err := loadMTLSConfig(GRPCConfig{})
	if err == nil {
		t.Fatal("expected error for missing mTLS files")
	}
}

func TestLoadMTLSConfigEnforcesClientCertVerification(t *testing.T) {
	dir := t.TempDir()
	caCert, caKey := mustNewCA(t)
	serverCertPEM, serverKeyPEM := mustNewServerCert(t, caCert, caKey)
	caPath := filepath.Join(dir, "ca.pem")
	certPath := filepath.Join(dir, "server.pem")
	keyPath := filepath.Join(dir, "server.key")
	if err := os.WriteFile(caPath, caCert, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(certPath, serverCertPEM, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyPath, serverKeyPEM, 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := loadMTLSConfig(GRPCConfig{
		TLSCertFile:     certPath,
		TLSKeyFile:      keyPath,
		TLSClientCAFile: caPath,
	})
	if err != nil {
		t.Fatalf("loadMTLSConfig: %v", err)
	}
	if cfg.ClientAuth != tls.RequireAndVerifyClientCert {
		t.Fatalf("expected ClientAuth RequireAndVerifyClientCert, got %v", cfg.ClientAuth)
	}
	if cfg.MinVersion != tls.VersionTLS13 {
		t.Fatalf("expected MinVersion TLS13, got %d", cfg.MinVersion)
	}
	if len(cfg.Certificates) != 1 {
		t.Fatalf("expected one server certificate, got %d", len(cfg.Certificates))
	}
}

func mustNewCA(t *testing.T) ([]byte, *rsa.PrivateKey) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	tpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "test-ca"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		IsCA:                  true,
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), key
}

func mustNewServerCert(t *testing.T, caPEM []byte, caKey *rsa.PrivateKey) ([]byte, []byte) {
	t.Helper()
	block, _ := pem.Decode(caPEM)
	if block == nil {
		t.Fatal("failed to decode CA PEM")
	}
	caCert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	serverKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	tpl := &x509.Certificate{
		SerialNumber: big.NewInt(2),
		Subject:      pkix.Name{CommonName: "localhost"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		DNSNames:     []string{"localhost"},
	}
	der, err := x509.CreateCertificate(rand.Reader, tpl, caCert, &serverKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	keyDER := x509.MarshalPKCS1PrivateKey(serverKey)
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: keyDER})
}

func TestGRPCShutdownGracefulStopAllowsInflightRPCToFinish(t *testing.T) {
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer lis.Close()

	s := &GRPCServer{server: grpc.NewServer(), lis: lis}
	registerSleepService(s.server, 200*time.Millisecond)
	go func() {
		_ = s.server.Serve(lis)
	}()
	t.Cleanup(func() { s.server.Stop() })

	conn, err := grpc.NewClient(lis.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	errCh := make(chan error, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		errCh <- conn.Invoke(ctx, "/chaos.Chaos/Sleep", &emptypb.Empty{}, &emptypb.Empty{})
	}()
	time.Sleep(50 * time.Millisecond)
	s.Shutdown(2 * time.Second)
	if err := <-errCh; err != nil {
		t.Fatalf("expected in-flight RPC to finish successfully, got %v", err)
	}
}

func TestGRPCShutdownTimeoutForcesStop(t *testing.T) {
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer lis.Close()

	s := &GRPCServer{server: grpc.NewServer(), lis: lis}
	registerSleepService(s.server, 500*time.Millisecond)
	go func() {
		_ = s.server.Serve(lis)
	}()
	t.Cleanup(func() { s.server.Stop() })

	conn, err := grpc.NewClient(lis.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	errCh := make(chan error, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		errCh <- conn.Invoke(ctx, "/chaos.Chaos/Sleep", &emptypb.Empty{}, &emptypb.Empty{})
	}()
	time.Sleep(50 * time.Millisecond)
	s.Shutdown(50 * time.Millisecond)
	if err := <-errCh; err == nil {
		t.Fatal("expected RPC failure when forced stop interrupts in-flight call")
	}
}

func TestPollerGRPCWireLifecycle(t *testing.T) {
	dir := t.TempDir()
	caCertPEM, caKey := mustNewCA(t)
	serverCertPEM, serverKeyPEM := mustNewServerCert(t, caCertPEM, caKey)
	clientCertPEM, clientKeyPEM := mustNewClientCert(t, caCertPEM, caKey)
	caPath := filepath.Join(dir, "ca.pem")
	serverCertPath := filepath.Join(dir, "server.pem")
	serverKeyPath := filepath.Join(dir, "server.key")
	clientCertPath := filepath.Join(dir, "client.pem")
	clientKeyPath := filepath.Join(dir, "client.key")
	for _, f := range []struct {
		path string
		data []byte
	}{
		{caPath, caCertPEM},
		{serverCertPath, serverCertPEM},
		{serverKeyPath, serverKeyPEM},
		{clientCertPath, clientCertPEM},
		{clientKeyPath, clientKeyPEM},
	} {
		if err := os.WriteFile(f.path, f.data, 0o600); err != nil {
			t.Fatal(err)
		}
	}

	db, err := storage.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := storage.Migrate(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Format(time.RFC3339)
	if _, err := db.Exec(`INSERT INTO tenants(id, name, created_at) VALUES(1, 't', ?)`, now); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO devices(id, tenant_id, hostname, address, port, driver, created_at) VALUES(10, 1, 'r1', '127.0.0.1', 22, 'generic_ssh', ?)`, now); err != nil {
		t.Fatal(err)
	}

	pollers := poller.New(db)
	jobs := poller.NewJobService(db)
	wire := poller.NewWireService(pollers, jobs)
	p, token, err := pollers.Register(context.Background(), 1, "z1", "poller-a")
	if err != nil {
		t.Fatal(err)
	}
	enq, err := jobs.Enqueue(context.Background(), 1, 10, poller.JobTypeBackup, "{}", "grpc-lifecycle", time.Minute)
	if err != nil {
		t.Fatal(err)
	}

	srv, err := NewGRPCServer(GRPCConfig{
		Address:         "127.0.0.1:0",
		TLSCertFile:     serverCertPath,
		TLSKeyFile:      serverKeyPath,
		TLSClientCAFile: caPath,
	}, wire, nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	errCh := make(chan error, 1)
	go func() { errCh <- srv.Start(ctx) }()
	t.Cleanup(func() { srv.Shutdown(time.Second) })

	clientCert, err := tls.LoadX509KeyPair(clientCertPath, clientKeyPath)
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(caCertPEM) {
		t.Fatal("append ca cert")
	}
	_, port, _ := net.SplitHostPort(srv.lis.Addr().String())
	target := net.JoinHostPort("localhost", port)
	conn, err := grpc.NewClient(target, grpc.WithTransportCredentials(credentials.NewTLS(&tls.Config{
		Certificates: []tls.Certificate{clientCert},
		RootCAs:      pool,
		MinVersion:   tls.VersionTLS13,
		ServerName:   "localhost",
	})))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	client := pollerv1.NewPollerServiceClient(conn)

	authResp, authCtx, err := waitForPollerAuthenticate(client, token, p.Name)
	if err != nil {
		t.Fatalf("Authenticate: %v", err)
	}
	if authResp.GetPoller().GetId() != p.ID {
		t.Fatalf("expected poller id %d, got %d", p.ID, authResp.GetPoller().GetId())
	}

	claimResp, err := client.ClaimJob(authCtx, &pollerv1.ClaimJobRequest{
		PollerId:          p.ID,
		TenantId:          1,
		SupportedJobTypes: []string{"backup"},
	})
	if err != nil {
		t.Fatalf("ClaimJob: %v", err)
	}
	if claimResp.GetJob().GetId() != enq.ID {
		t.Fatalf("expected claimed job %d, got %d", enq.ID, claimResp.GetJob().GetId())
	}

	reportResp, err := client.ReportResult(authCtx, &pollerv1.ReportResultRequest{
		JobId:      claimResp.GetJob().GetId(),
		PollerId:   p.ID,
		TenantId:   1,
		Success:    true,
		ResultJson: `{"applied":true}`,
	})
	if err != nil {
		t.Fatalf("ReportResult: %v", err)
	}
	if !reportResp.GetAccepted() {
		t.Fatal("expected accepted=true")
	}

	health, err := client.Health(authCtx, &pollerv1.HealthRequest{PollerId: p.ID, TenantId: 1})
	if err != nil {
		t.Fatalf("Health: %v", err)
	}
	if !health.GetHealthy() {
		t.Fatal("expected healthy=true")
	}

	stream, err := client.StreamJobs(authCtx, &pollerv1.StreamJobsRequest{
		PollerId: p.ID, TenantId: 1, SupportedJobTypes: []string{"backup"},
	})
	if err != nil {
		t.Fatalf("StreamJobs: %v", err)
	}
	if _, err := stream.Recv(); err != nil {
		t.Fatalf("StreamJobs recv: %v", err)
	}

	var status string
	if err := db.QueryRow(`SELECT status FROM poller_jobs WHERE id=?`, enq.ID).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if strings.ToLower(status) != "done" {
		t.Fatalf("expected job status done, got %q", status)
	}

	cancel()
	if err := <-errCh; err != nil {
		t.Fatalf("grpc server returned error: %v", err)
	}
}

type sleepService struct {
	delay time.Duration
}

type sleepServer interface {
	Sleep(ctx context.Context, in *emptypb.Empty) (*emptypb.Empty, error)
}

func (s sleepService) Sleep(ctx context.Context, _ *emptypb.Empty) (*emptypb.Empty, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-time.After(s.delay):
		return &emptypb.Empty{}, nil
	}
}

func registerSleepService(server *grpc.Server, delay time.Duration) {
	svc := sleepService{delay: delay}
	server.RegisterService(&grpc.ServiceDesc{
		ServiceName: "chaos.Chaos",
		HandlerType: (*sleepServer)(nil),
		Methods: []grpc.MethodDesc{{
			MethodName: "Sleep",
			Handler: func(srv any, ctx context.Context, dec func(any) error, _ grpc.UnaryServerInterceptor) (any, error) {
				var in emptypb.Empty
				if err := dec(&in); err != nil {
					return nil, err
				}
				return srv.(sleepServer).Sleep(ctx, &in)
			},
		}},
	}, svc)
}

func waitForPollerAuthenticate(client pollerv1.PollerServiceClient, token, pollerName string) (*pollerv1.AuthenticateResponse, context.Context, error) {
	deadline := time.Now().Add(3 * time.Second)
	var lastErr error
	for time.Now().Before(deadline) {
		nowNano := time.Now().UnixNano()
		nonce := fmt.Sprintf("%d%08x", nowNano, nowNano)
		authCtx := metadata.AppendToOutgoingContext(context.Background(), "authorization", "Bearer "+token)
		resp, err := client.Authenticate(authCtx, &pollerv1.AuthenticateRequest{
			PollerName: pollerName,
			TenantId:   1,
			ClientTime: timestamppb.Now(),
			Nonce:      nonce,
		})
		if err == nil {
			return resp, authCtx, nil
		}
		lastErr = err
		time.Sleep(50 * time.Millisecond)
	}
	return nil, nil, lastErr
}

func mustNewClientCert(t *testing.T, caPEM []byte, caKey *rsa.PrivateKey) ([]byte, []byte) {
	t.Helper()
	block, _ := pem.Decode(caPEM)
	if block == nil {
		t.Fatal("failed to decode CA PEM")
	}
	caCert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	clientKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	tpl := &x509.Certificate{
		SerialNumber: big.NewInt(3),
		Subject:      pkix.Name{CommonName: "poller-client"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
	}
	der, err := x509.CreateCertificate(rand.Reader, tpl, caCert, &clientKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	keyDER := x509.MarshalPKCS1PrivateKey(clientKey)
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: keyDER})
}
