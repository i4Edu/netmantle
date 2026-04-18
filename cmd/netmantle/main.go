// Command netmantle is the NetMantle core API server.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/i4Edu/netmantle/internal/api"
	"github.com/i4Edu/netmantle/internal/auth"
	"github.com/i4Edu/netmantle/internal/backup"
	"github.com/i4Edu/netmantle/internal/config"
	"github.com/i4Edu/netmantle/internal/configstore"
	"github.com/i4Edu/netmantle/internal/credentials"
	"github.com/i4Edu/netmantle/internal/crypto"
	"github.com/i4Edu/netmantle/internal/devices"
	"github.com/i4Edu/netmantle/internal/drivers"
	_ "github.com/i4Edu/netmantle/internal/drivers/builtin"
	"github.com/i4Edu/netmantle/internal/logging"
	"github.com/i4Edu/netmantle/internal/observability"
	"github.com/i4Edu/netmantle/internal/storage"
	"github.com/i4Edu/netmantle/internal/transport"
	"github.com/i4Edu/netmantle/internal/version"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	switch os.Args[1] {
	case "serve":
		if err := runServe(os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			os.Exit(1)
		}
	case "version", "-v", "--version":
		fmt.Println("netmantle", version.Version)
	case "help", "-h", "--help":
		usage()
	default:
		usage()
		os.Exit(2)
	}
}

func usage() {
	fmt.Fprintf(os.Stderr, `netmantle %s

Usage:
  netmantle serve   [--config FILE]
  netmantle version
`, version.Version)
}

func runServe(argv []string) error {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	cfgPath := fs.String("config", "", "path to config file (yaml)")
	if err := fs.Parse(argv); err != nil {
		return err
	}

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		return err
	}
	log := logging.Setup(cfg.Logging.Level, cfg.Logging.Format).With(
		"service", "netmantle", "version", version.Version,
	)

	db, err := storage.Open(cfg.Database.Driver, cfg.Database.DSN)
	if err != nil {
		return err
	}
	defer db.Close()

	mctx, mcancel := context.WithTimeout(context.Background(), 30*time.Second)
	if err := storage.Migrate(mctx, db); err != nil {
		mcancel()
		return err
	}
	mcancel()

	sealer, err := crypto.NewSealer(cfg.Security.MasterPassphrase)
	if err != nil {
		return err
	}

	authSvc, err := auth.NewService(db, cfg.Security.SessionKey, cfg.Security.SessionCookie, cfg.Security.SessionTTL)
	if err != nil {
		return err
	}
	if user, pw, created, err := authSvc.EnsureBootstrapAdmin(context.Background(), os.Getenv("NETMANTLE_BOOTSTRAP_ADMIN_PASSWORD")); err != nil {
		return err
	} else if created {
		if os.Getenv("NETMANTLE_BOOTSTRAP_ADMIN_PASSWORD") == "" {
			log.Warn("bootstrapped admin user — capture this password, it will not be shown again",
				"username", user, "password", pw)
		} else {
			log.Info("bootstrapped admin user using preset password", "username", user)
		}
	}

	devRepo := devices.NewRepo(db)
	credRepo := credentials.NewRepo(db, sealer)
	store, err := configstore.New(cfg.Storage.ConfigRepoRoot)
	if err != nil {
		return err
	}

	sessionFactory := func(ctx context.Context, d devices.Device, user, pw string) (drivers.Session, func() error, error) {
		return transport.DialSSH(ctx, transport.SSHConfig{
			Address: d.Address, Port: d.Port,
			Username: user, Password: pw, Timeout: cfg.Backup.Timeout,
		})
	}
	bSvc := backup.New(devRepo, credRepo, store, db, log,
		cfg.Backup.Timeout, cfg.Backup.Workers, sessionFactory)

	metrics := observability.New()
	handler := api.NewServer(api.Deps{
		Auth: authSvc, Devices: devRepo, Credentials: credRepo,
		Backup: bSvc, Logger: log, Metrics: metrics,
	})

	srv := &http.Server{
		Addr:              cfg.Server.Address,
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       cfg.Server.ReadTimeout,
		WriteTimeout:      cfg.Server.WriteTimeout,
		IdleTimeout:       60 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	go func() {
		log.Info("listening", "addr", cfg.Server.Address)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Error("server error", "err", err)
			stop()
		}
	}()
	<-ctx.Done()
	log.Info("shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		return err
	}
	_ = slog.Default()
	return nil
}
