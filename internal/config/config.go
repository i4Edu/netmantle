// Package config loads NetMantle configuration from a YAML file and from
// environment variables. Env vars use the prefix NETMANTLE_ and follow a
// hand-maintained allowlist (see applyEnv); the most common knobs are
// NETMANTLE_SERVER_ADDRESS, NETMANTLE_DATABASE_DSN,
// NETMANTLE_STORAGE_CONFIG_REPO_ROOT and
// NETMANTLE_SECURITY_MASTER_PASSPHRASE. Anything not listed in applyEnv
// must be set in the YAML file.
package config

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Config is the top-level configuration tree.
type Config struct {
	Server   ServerConfig   `yaml:"server"`
	Database DatabaseConfig `yaml:"database"`
	Storage  StorageConfig  `yaml:"storage"`
	Security SecurityConfig `yaml:"security"`
	Logging  LoggingConfig  `yaml:"logging"`
	Backup   BackupConfig   `yaml:"backup"`
	Poller   PollerConfig   `yaml:"poller"`
}

type ServerConfig struct {
	Address      string        `yaml:"address"`
	ReadTimeout  time.Duration `yaml:"read_timeout"`
	WriteTimeout time.Duration `yaml:"write_timeout"`
}

type DatabaseConfig struct {
	Driver string `yaml:"driver"`
	DSN    string `yaml:"dsn"`
}

type StorageConfig struct {
	ConfigRepoRoot string `yaml:"config_repo_root"`
}

type SecurityConfig struct {
	MasterPassphrase string        `yaml:"master_passphrase"`
	SessionCookie    string        `yaml:"session_cookie"`
	SessionKey       string        `yaml:"session_key"`
	SessionTTL       time.Duration `yaml:"session_ttl"`
}

type LoggingConfig struct {
	Level  string `yaml:"level"`
	Format string `yaml:"format"`
}

type BackupConfig struct {
	Timeout time.Duration `yaml:"timeout"`
	Workers int           `yaml:"workers"`
}

type PollerConfig struct {
	GRPC PollerGRPCConfig `yaml:"grpc"`
}

type PollerGRPCConfig struct {
	Address         string `yaml:"address"`
	TLSCertFile     string `yaml:"tls_cert_file"`
	TLSKeyFile      string `yaml:"tls_key_file"`
	TLSClientCAFile string `yaml:"tls_client_ca_file"`
}

// Default returns a Config populated with sensible defaults.
func Default() Config {
	return Config{
		Server: ServerConfig{
			Address:      ":8080",
			ReadTimeout:  30 * time.Second,
			WriteTimeout: 30 * time.Second,
		},
		Database: DatabaseConfig{
			Driver: "sqlite",
			DSN:    "data/netmantle.db",
		},
		Storage: StorageConfig{
			ConfigRepoRoot: "data/configs",
		},
		Security: SecurityConfig{
			SessionCookie: "netmantle_session",
			SessionTTL:    24 * time.Hour,
		},
		Logging: LoggingConfig{
			Level:  "info",
			Format: "json",
		},
		Backup: BackupConfig{
			Timeout: 60 * time.Second,
			Workers: 4,
		},
		Poller: PollerConfig{
			GRPC: PollerGRPCConfig{
				Address: "",
			},
		},
	}
}

// Load reads configuration from the supplied path (may be empty), then
// applies environment overrides, then validates the result.
func Load(path string) (Config, error) {
	cfg := Default()
	if path != "" {
		b, err := os.ReadFile(path)
		if err != nil {
			return cfg, fmt.Errorf("read config: %w", err)
		}
		if err := yaml.Unmarshal(b, &cfg); err != nil {
			return cfg, fmt.Errorf("parse config: %w", err)
		}
	}
	applyEnv(&cfg)
	if err := cfg.Validate(); err != nil {
		return cfg, err
	}
	return cfg, nil
}

// Validate checks invariants. Required values that have no safe default
// (e.g. master passphrase) must be set either in the file or via env.
func (c Config) Validate() error {
	if c.Server.Address == "" {
		return errors.New("server.address must be set")
	}
	if c.Database.Driver != "sqlite" {
		return fmt.Errorf("unsupported database.driver %q (only sqlite supported in this build)", c.Database.Driver)
	}
	if c.Database.DSN == "" {
		return errors.New("database.dsn must be set")
	}
	if c.Storage.ConfigRepoRoot == "" {
		return errors.New("storage.config_repo_root must be set")
	}
	if strings.TrimSpace(c.Security.MasterPassphrase) == "" {
		return errors.New("security.master_passphrase must be set (env: NETMANTLE_SECURITY_MASTER_PASSPHRASE)")
	}
	if c.Backup.Workers < 1 {
		return errors.New("backup.workers must be >= 1")
	}
	grpcCfg := c.Poller.GRPC
	if grpcCfg.Address != "" {
		if strings.TrimSpace(grpcCfg.TLSCertFile) == "" {
			return errors.New("poller.grpc.tls_cert_file must be set when poller.grpc.address is enabled")
		}
		if strings.TrimSpace(grpcCfg.TLSKeyFile) == "" {
			return errors.New("poller.grpc.tls_key_file must be set when poller.grpc.address is enabled")
		}
		if strings.TrimSpace(grpcCfg.TLSClientCAFile) == "" {
			return errors.New("poller.grpc.tls_client_ca_file must be set when poller.grpc.address is enabled")
		}
	}
	return nil
}

func applyEnv(c *Config) {
	if v := os.Getenv("NETMANTLE_SERVER_ADDRESS"); v != "" {
		c.Server.Address = v
	}
	if v := os.Getenv("NETMANTLE_DATABASE_DRIVER"); v != "" {
		c.Database.Driver = v
	}
	if v := os.Getenv("NETMANTLE_DATABASE_DSN"); v != "" {
		c.Database.DSN = v
	}
	if v := os.Getenv("NETMANTLE_STORAGE_CONFIG_REPO_ROOT"); v != "" {
		c.Storage.ConfigRepoRoot = v
	}
	if v := os.Getenv("NETMANTLE_SECURITY_MASTER_PASSPHRASE"); v != "" {
		c.Security.MasterPassphrase = v
	}
	if v := os.Getenv("NETMANTLE_SECURITY_SESSION_KEY"); v != "" {
		c.Security.SessionKey = v
	}
	if v := os.Getenv("NETMANTLE_LOGGING_LEVEL"); v != "" {
		c.Logging.Level = v
	}
	if v := os.Getenv("NETMANTLE_LOGGING_FORMAT"); v != "" {
		c.Logging.Format = v
	}
	if v := os.Getenv("NETMANTLE_BACKUP_WORKERS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			c.Backup.Workers = n
		}
	}
	if v := os.Getenv("NETMANTLE_BACKUP_TIMEOUT"); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			c.Backup.Timeout = d
		}
	}
	if v := os.Getenv("NETMANTLE_POLLER_GRPC_ADDRESS"); v != "" {
		c.Poller.GRPC.Address = v
	}
	if v := os.Getenv("NETMANTLE_POLLER_GRPC_TLS_CERT_FILE"); v != "" {
		c.Poller.GRPC.TLSCertFile = v
	}
	if v := os.Getenv("NETMANTLE_POLLER_GRPC_TLS_KEY_FILE"); v != "" {
		c.Poller.GRPC.TLSKeyFile = v
	}
	if v := os.Getenv("NETMANTLE_POLLER_GRPC_TLS_CLIENT_CA_FILE"); v != "" {
		c.Poller.GRPC.TLSClientCAFile = v
	}
}
