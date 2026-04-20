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
	"strings"
	"syscall"
	"time"

	"github.com/i4Edu/netmantle/internal/api"
	"github.com/i4Edu/netmantle/internal/apitokens"
	"github.com/i4Edu/netmantle/internal/audit"
	"github.com/i4Edu/netmantle/internal/auth"
	"github.com/i4Edu/netmantle/internal/automation"
	"github.com/i4Edu/netmantle/internal/backup"
	"github.com/i4Edu/netmantle/internal/changereq"
	"github.com/i4Edu/netmantle/internal/changes"
	"github.com/i4Edu/netmantle/internal/compliance"
	"github.com/i4Edu/netmantle/internal/config"
	"github.com/i4Edu/netmantle/internal/configstore"
	"github.com/i4Edu/netmantle/internal/credentials"
	"github.com/i4Edu/netmantle/internal/crypto"
	"github.com/i4Edu/netmantle/internal/devices"
	"github.com/i4Edu/netmantle/internal/diff"
	"github.com/i4Edu/netmantle/internal/discovery"
	"github.com/i4Edu/netmantle/internal/drivers"
	_ "github.com/i4Edu/netmantle/internal/drivers/builtin"
	"github.com/i4Edu/netmantle/internal/gitops"
	"github.com/i4Edu/netmantle/internal/logging"
	"github.com/i4Edu/netmantle/internal/notify"
	"github.com/i4Edu/netmantle/internal/observability"
	"github.com/i4Edu/netmantle/internal/poller"
	"github.com/i4Edu/netmantle/internal/probes"
	"github.com/i4Edu/netmantle/internal/scheduler"
	"github.com/i4Edu/netmantle/internal/search"
	grpcserver "github.com/i4Edu/netmantle/internal/server"
	"github.com/i4Edu/netmantle/internal/storage"
	"github.com/i4Edu/netmantle/internal/tenants"
	"github.com/i4Edu/netmantle/internal/terminal"
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

	// Persistent SSH known-hosts store (closes threat-model gap T7).
	// Tenant-scoped: each device's host key is pinned under the owning
	// tenant's ID so cross-tenant key pollution is impossible.
	knownHosts := transport.NewDBKnownHostsStore(db)

	sessionFactory := func(ctx context.Context, d devices.Device, user, pw string) (drivers.Session, func() error, error) {
		return transport.DialSSH(ctx, transport.SSHConfig{
			Address:    d.Address,
			Port:       d.Port,
			Username:   user,
			Password:   pw,
			Timeout:    cfg.Backup.Timeout,
			KnownHosts: knownHosts,
			TenantID:   d.TenantID,
		})
	}

	// NETCONF factory used for cisco_netconf / junos_netconf devices.
	// Routes through the dedicated SSH NETCONF subsystem (RFC 6242) rather
	// than the interactive CLI shell used by CLI drivers.
	netconfFactory := func(ctx context.Context, d devices.Device, user, pw string) (drivers.Session, func() error, error) {
		sess, closer, err := transport.DialNetconf(ctx, transport.NetconfConfig{
			Address:    d.Address,
			Port:       d.Port,
			Username:   user,
			Password:   pw,
			Timeout:    cfg.Backup.Timeout,
			KnownHosts: knownHosts,
			TenantID:   d.TenantID,
		})
		if err != nil {
			return nil, nil, err
		}
		return sess, closer, nil
	}

	restconfFactory := func(ctx context.Context, d devices.Device, user, pw string) (drivers.Session, func() error, error) {
		bearer := ""
		if strings.EqualFold(strings.TrimSpace(user), "bearer") || strings.EqualFold(strings.TrimSpace(user), "token") {
			bearer = pw
			user = ""
			pw = ""
		}
		sess, closer, err := transport.DialRESTCONF(ctx, transport.RESTCONFConfig{
			Address:     d.Address,
			Port:        d.Port,
			Username:    user,
			Password:    pw,
			BearerToken: bearer,
			Timeout:     cfg.Backup.Timeout,
		})
		if err != nil {
			return nil, nil, err
		}
		return sess, closer, nil
	}

	gnmiFactory := func(ctx context.Context, d devices.Device, user, pw string) (drivers.Session, func() error, error) {
		bearer := ""
		if strings.EqualFold(strings.TrimSpace(user), "bearer") || strings.EqualFold(strings.TrimSpace(user), "token") {
			bearer = pw
			user = ""
			pw = ""
		}
		sess, closer, err := transport.DialGNMI(ctx, transport.GNMIConfig{
			Address:     d.Address,
			Port:        d.Port,
			Username:    user,
			Password:    pw,
			BearerToken: bearer,
			Timeout:     cfg.Backup.Timeout,
		})
		if err != nil {
			return nil, nil, err
		}
		return sess, closer, nil
	}

	auditSvc := audit.New(db, log)
	bSvc := backup.New(devRepo, credRepo, store, db, log,
		cfg.Backup.Timeout, cfg.Backup.Workers, sessionFactory)
	bSvc.Audit = auditSvc
	bSvc.NetconfSession = netconfFactory
	bSvc.RestconfSession = restconfFactory
	bSvc.GNMISession = gnmiFactory

	// Phase 2..10 services.
	chgSvc := changes.New(db, store, &diff.Engine{Rules: diff.DefaultRules()})
	notifySvc := notify.New(db, sealer, log)
	searchSvc := search.New(db)
	complianceSvc := compliance.New(db)
	discoverySvc := discovery.New(db)
	probesSvc := probes.New(db)
	tenantsSvc := tenants.New(db)
	pollerSvc := poller.New(db)
	pollerJobsSvc := poller.NewJobService(db)
	wireSvc := poller.NewWireService(pollerSvc, pollerJobsSvc)
	gitopsSvc := gitops.New(db, store, sealer)
	terminalSvc := terminal.New(db, func(ctx context.Context, tenantID, deviceID int64) (terminal.Backend, error) {
		dev, err := devRepo.GetDevice(ctx, tenantID, deviceID)
		if err != nil {
			return nil, err
		}
		if dev.CredentialID == nil {
			return nil, errors.New("device has no credential")
		}
		// Use scopes the cleartext to the dial call: the SSH transport
		// holds the negotiated session, so the password byte slice can
		// be zeroised immediately on return.
		var backend terminal.Backend
		err = credRepo.Use(ctx, tenantID, *dev.CredentialID, func(user, pw string) error {
			b, derr := transport.DialSSHShell(ctx, transport.SSHConfig{
				Address:    dev.Address,
				Port:       dev.Port,
				Username:   user,
				Password:   pw,
				Timeout:    cfg.Backup.Timeout,
				KnownHosts: knownHosts,
				TenantID:   tenantID,
			})
			if derr != nil {
				return derr
			}
			backend = b
			return nil
		})
		if err != nil {
			return nil, err
		}
		return backend, nil
	})
	automationSvc := automation.New(db, devRepo, func(ctx context.Context, d devices.Device, _ string) (string, error) {
		// Live execution requires a per-driver Apply hook; this MVP returns
		// a clear error so users can still use Preview + GroupResults.
		return "", errors.New("automation: live execution requires per-driver Apply (Phase 6 follow-up)")
	})

	// Wire post-backup hooks: detect changes, index for search, evaluate
	// compliance, dispatch notifications, optionally mirror.
	bSvc.PostCommit = []backup.PostCommitHook{
		func(ctx context.Context, tenantID int64, dev devices.Device, sha string, arts []configstore.Artifact) {
			for _, a := range arts {
				ev, err := chgSvc.Record(ctx, tenantID, dev.ID, a.Name, sha)
				if err != nil {
					log.Warn("changes: record", "err", err)
				} else if ev != nil {
					notifySvc.Dispatch(ctx, tenantID, notify.Event{
						Kind: "change.detected", Subject: dev.Hostname,
						Body: fmt.Sprintf("%s: +%d/-%d", a.Name, ev.Added, ev.Removed),
					})
				}
				if err := searchSvc.Index(ctx, tenantID, dev.ID, a.Name, sha, a.Content); err != nil {
					log.Warn("search: index", "err", err)
				}
				if _, err := complianceSvc.EvaluateDevice(ctx, tenantID, dev.ID, string(a.Content)); err != nil {
					log.Warn("compliance: eval", "err", err)
				}
			}
			if err := gitopsSvc.PushDevice(ctx, tenantID, dev.ID); err != nil {
				log.Warn("gitops: mirror push", "err", err)
			}
		},
	}
	complianceSvc.OnTransition = func(ctx context.Context, tenantID int64, f compliance.Finding, prev string) {
		notifySvc.Dispatch(ctx, tenantID, notify.Event{
			Kind:    "compliance.transition",
			Subject: fmt.Sprintf("rule:%d device:%d", f.RuleID, f.DeviceID),
			Body:    fmt.Sprintf("%s → %s: %s", prev, f.Status, f.Detail),
		})
	}

	metrics := observability.New()
	changeReqSvc := changereq.New(db)
	apiTokensSvc := apitokens.New(db)
	handler := api.NewServer(api.Deps{
		Auth: authSvc, Devices: devRepo, Credentials: credRepo,
		Backup: bSvc, Logger: log, Metrics: metrics, Audit: auditSvc,
		Changes: chgSvc, Notify: notifySvc, Search: searchSvc,
		Compliance: complianceSvc, Discovery: discoverySvc,
		Automation: automationSvc, Probes: probesSvc,
		Tenants: tenantsSvc, Pollers: pollerSvc,
		Terminal: terminalSvc, GitOps: gitopsSvc,
		ChangeReq: changeReqSvc, APITokens: apiTokensSvc,
		DB: db,
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

	// Start leader-elected scheduler (Phase 9). Replicas race for the
	// "scheduled-jobs" lease; the holder runs the jobs.
	runner := &scheduler.Runner{
		Lease: scheduler.NewLease(db, "scheduled-jobs", 30*time.Second),
		Jobs: []scheduler.Job{
			{
				Name: "probe-retention", Interval: time.Hour,
				Run: func(ctx context.Context) error {
					_, err := probesSvc.PruneOlderThan(ctx, time.Now().Add(-30*24*time.Hour))
					return err
				},
			},
		},
	}
	go runner.Start(ctx, log.Info)

	go func() {
		log.Info("listening", "addr", cfg.Server.Address)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Error("server error", "err", err)
			stop()
		}
	}()
	var grpcSrv *grpcserver.GRPCServer
	if cfg.Poller.GRPC.Address != "" {
		gs, err := grpcserver.NewGRPCServer(grpcserver.GRPCConfig{
			Address:         cfg.Poller.GRPC.Address,
			TLSCertFile:     cfg.Poller.GRPC.TLSCertFile,
			TLSKeyFile:      cfg.Poller.GRPC.TLSKeyFile,
			TLSClientCAFile: cfg.Poller.GRPC.TLSClientCAFile,
		}, wireSvc, log)
		if err != nil {
			return err
		}
		grpcSrv = gs
		go func() {
			if err := grpcSrv.Start(ctx); err != nil {
				log.Error("poller grpc server error", "err", err)
				stop()
			}
		}()
	}
	<-ctx.Done()
	log.Info("shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		return err
	}
	if grpcSrv != nil {
		grpcSrv.Shutdown(5 * time.Second)
	}
	_ = slog.Default()
	return nil
}
