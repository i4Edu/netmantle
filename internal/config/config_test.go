package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDefaultsAndValidate(t *testing.T) {
	c := Default()
	if err := c.Validate(); err == nil {
		t.Fatal("expected validation error for missing master passphrase")
	}
	c.Security.MasterPassphrase = "ok"
	if err := c.Validate(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestLoadFileAndEnvOverride(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "c.yaml")
	if err := os.WriteFile(p, []byte("server:\n  address: \":1234\"\nsecurity:\n  master_passphrase: filepass\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("NETMANTLE_SECURITY_MASTER_PASSPHRASE", "envpass")
	t.Setenv("NETMANTLE_SERVER_ADDRESS", ":9999")
	t.Setenv("NETMANTLE_POLLER_GRPC_ADDRESS", ":9443")
	t.Setenv("NETMANTLE_POLLER_GRPC_TLS_CERT_FILE", "/tmp/s.crt")
	t.Setenv("NETMANTLE_POLLER_GRPC_TLS_KEY_FILE", "/tmp/s.key")
	t.Setenv("NETMANTLE_POLLER_GRPC_TLS_CLIENT_CA_FILE", "/tmp/ca.crt")

	c, err := Load(p)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if c.Server.Address != ":9999" {
		t.Errorf("env should override file: %q", c.Server.Address)
	}
	if c.Security.MasterPassphrase != "envpass" {
		t.Errorf("env should override file: %q", c.Security.MasterPassphrase)
	}
	if c.Poller.GRPC.Address != ":9443" {
		t.Errorf("env should override file: %q", c.Poller.GRPC.Address)
	}
}

func TestValidateRejectsUnsupportedDriver(t *testing.T) {
	c := Default()
	c.Security.MasterPassphrase = "x"
	c.Database.Driver = "mysql"
	if err := c.Validate(); err == nil {
		t.Fatal("expected error for unsupported driver")
	}
}

func TestValidatePollerGRPCRequiresMTLSFiles(t *testing.T) {
	c := Default()
	c.Security.MasterPassphrase = "x"
	c.Poller.GRPC.Address = ":9443"
	if err := c.Validate(); err == nil {
		t.Fatal("expected validation error for missing poller grpc mTLS files")
	}
	c.Poller.GRPC.TLSCertFile = "/tmp/s.crt"
	c.Poller.GRPC.TLSKeyFile = "/tmp/s.key"
	c.Poller.GRPC.TLSClientCAFile = "/tmp/ca.crt"
	if err := c.Validate(); err != nil {
		t.Fatalf("unexpected validation error: %v", err)
	}
}
