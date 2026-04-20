package server

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/protobuf/types/known/emptypb"
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

type sleepService struct {
	delay time.Duration
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
		HandlerType: (*sleepService)(nil),
		Methods: []grpc.MethodDesc{{
			MethodName: "Sleep",
			Handler: func(srv any, ctx context.Context, dec func(any) error, _ grpc.UnaryServerInterceptor) (any, error) {
				var in emptypb.Empty
				if err := dec(&in); err != nil {
					return nil, err
				}
				return srv.(sleepService).Sleep(ctx, &in)
			},
		}},
	}, svc)
}
