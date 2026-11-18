// Command workbench is the Workbench server: kagent's controller with
// Workbench's authenticator, authorizer, tables, gRPC services and MCP tools
// compiled in. One process, one API, one database, one audit stream.
//
// It reads kagent's controller configuration exactly as kagent's own binary
// does, plus a Workbench configuration file.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/smtp"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	configv1 "github.com/kagent-dev/kagent/go/api/gen/kagent/config/v1"
	"github.com/kagent-dev/kagent/go/core/pkg/app"
	kconfig "github.com/kagent-dev/kagent/go/core/pkg/config"
	"github.com/kagent-dev/kagent/go/core/pkg/migrations"
	"github.com/kagent-dev/kagent/go/core/pkg/postgres"
	"golang.org/x/oauth2/google"
	"golang.org/x/sync/errgroup"
	"sigs.k8s.io/controller-runtime/pkg/manager"

	"github.com/thehaymaker/kagent-porcelain/server/internal/api"
	"github.com/thehaymaker/kagent-porcelain/server/internal/approval"
	"github.com/thehaymaker/kagent-porcelain/server/internal/audit"
	"github.com/thehaymaker/kagent-porcelain/server/internal/authz"
	"github.com/thehaymaker/kagent-porcelain/server/internal/components"
	"github.com/thehaymaker/kagent-porcelain/server/internal/config"
	"github.com/thehaymaker/kagent-porcelain/server/internal/hooks"
	"github.com/thehaymaker/kagent-porcelain/server/internal/identity"
	"github.com/thehaymaker/kagent-porcelain/server/internal/kagent"
	"github.com/thehaymaker/kagent-porcelain/server/internal/mcptools"
	"github.com/thehaymaker/kagent-porcelain/server/internal/notify"
	"github.com/thehaymaker/kagent-porcelain/server/internal/registry"
	"github.com/thehaymaker/kagent-porcelain/server/internal/store"
	"github.com/thehaymaker/kagent-porcelain/server/internal/usage"
	wbmigrations "github.com/thehaymaker/kagent-porcelain/server/migrations"
)

func main() {
	kagentPath := flag.String("config", "/etc/kagent/controller.yaml", "kagent controller configuration")
	wbPath := flag.String("workbench-config", "/etc/workbench/workbench.yaml", "Workbench configuration")
	check := flag.Bool("check-config", false, "validate both configurations and exit")
	flag.Parse()
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, *kagentPath, *wbPath, *check); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(ctx context.Context, kagentPath, wbPath string, check bool) error {
	kcfg := &configv1.ControllerConfig{}
	if err := kconfig.Load(kagentPath, kcfg); err != nil {
		return err
	}
	kconfig.DefaultController(kcfg)
	if err := kconfig.Validate(kcfg); err != nil {
		return err
	}
	kconfig.ResolveControllerPaths(kcfg, kagentPath)
	cfg, err := config.Load(wbPath)
	if err != nil {
		return err
	}
	if check {
		return nil
	}
	log := slog.Default()

	internalKey, err := readSecret(cfg.Identity.InternalKeyFile)
	if err != nil {
		return fmt.Errorf("identity.internalKeyFile: %w", err)
	}

	// Workbench's tables share kagent's database and login, in their own
	// schema. kagent migrates them (ExtraMigrations) before serving.
	dbURL, err := kconfig.ReadSecret(kcfg.GetDatabase().GetReadWrite().GetUrlFile())
	if err != nil {
		return fmt.Errorf("database.readWrite.urlFile: %w", err)
	}
	// Same login and role as kagent's read/write pool; see
	// deploy/database/grants.sql for the privileges that role needs.
	poolCfg, err := postgres.PoolConfig(strings.TrimSpace(dbURL), kcfg.GetDatabase().GetReadWrite().GetRole())
	if err != nil {
		return fmt.Errorf("database.readWrite: %w", err)
	}
	poolCfg.ConnConfig.RuntimeParams["search_path"] = pgx.Identifier{wbmigrations.Schema}.Sanitize()
	pool, err := pgxpool.NewWithConfig(ctx, poolCfg)
	if err != nil {
		return err
	}
	defer pool.Close()
	db := store.New(pool)

	pol := cfg.Policy()
	authorizer, err := authz.New(cfg.Authorization, cfg.ApproverGroups())
	if err != nil {
		return err
	}
	authn, err := identity.New(cfg.Identity.Issuers, identity.NewDiscoveryKeySet(ctx, nil), &links{store: db, seen: map[string]time.Time{}}, internalKey)
	if err != nil {
		return err
	}
	authn.SystemGroups = cfg.Authorization.PlatformGroups

	kc, err := kagent.Dial(kcfg.GetServer().GetBindAddress(), authn)
	if err != nil {
		return err
	}
	defer kc.Close()

	recorder := audit.StoreRecorder{Store: db, Log: log}
	notifier := &notify.Notifier{Store: db, BaseURL: cfg.PublicURL, Log: log}
	googleClient := func() *http.Client {
		c, err := google.DefaultClient(ctx, "https://www.googleapis.com/auth/cloud-platform")
		if err != nil {
			log.Error("google credentials unavailable", "error", err)
			return http.DefaultClient
		}
		return c
	}
	if s := cfg.Notifications.SMTP; s != nil {
		notifier.Deliverers = append(notifier.Deliverers, notify.SMTP{Addr: s.Addr, From: s.From, Auth: smtpAuth(s.Addr)})
	}
	if t := cfg.Notifications.Teams; t != nil {
		secret, err := readSecret(t.SecretFile)
		if err != nil {
			return fmt.Errorf("notifications.teams.secretFile: %w", err)
		}
		notifier.Deliverers = append(notifier.Deliverers, notify.Webhook{Name: notify.Teams, URL: t.URL, Secret: secret})
	}
	if cfg.Notifications.FCMProject != "" {
		notifier.Deliverers = append(notifier.Deliverers, notify.FCM{Project: cfg.Notifications.FCMProject, Client: googleClient(), Store: db})
	}

	var deployer registry.Deployer = registry.LogDeployer{}
	if cfg.Production.Deployer == "cloudrun" {
		deployer = &registry.CloudRun{Config: cfg.Production.CloudRun, Client: googleClient()}
	} else {
		log.Warn("production deployer is \"log\": promotions are recorded but nothing is deployed")
	}
	reg := &registry.Registry{Store: db, Deployer: deployer, Audit: recorder, Notify: notifier}
	approvals := &approval.Service{Store: db, Policy: pol, Kagent: kc, Audit: recorder, Notify: notifier, Promoter: reg,
		CallbackURL: cfg.Hooks.CallbackURL, Log: log}
	validator, err := components.NewValidator()
	if err != nil {
		return err
	}
	meter := &usage.Meter{Store: db, Notify: notifier}
	server := &api.Server{Store: db, Authz: authorizer, Approvals: approvals, Registry: reg, Meter: meter, Components: validator,
		Links: authn, Kagent: kc, Audit: recorder, Policy: pol}
	tools := &mcptools.Tools{Store: db, Registry: reg, Approvals: approvals, Components: validator, Audit: recorder}

	hookHandler := &hooks.Handler{Approvals: approvals, Meter: meter, PushIssuer: cfg.Hooks.PushIssuer, PushURL: cfg.Hooks.CallbackURL,
		Log: log, Ready: func(ctx context.Context) error { return pool.Ping(ctx) }}
	if cfg.Hooks.CallbackURL != "" {
		hookHandler.PushKeys = hooks.RemoteKeys(ctx, "http://"+loopback(kcfg.GetServer().GetBindAddress())+"/.well-known/jwks.json")
	}
	if cfg.Hooks.UsageSecretFile != "" {
		if hookHandler.UsageSecret, err = readSecret(cfg.Hooks.UsageSecretFile); err != nil {
			return fmt.Errorf("hooks.usageSecretFile: %w", err)
		}
	}

	opts := app.Options{
		Authenticator: authn,
		Authorizer:    authorizer,
		ExtraMigrations: []migrations.Source{{Name: "workbench", Schema: wbmigrations.Schema,
			TrackingTable: wbmigrations.TrackingTable, FS: wbmigrations.FS, Dir: "."}},
		GRPCServices:   server.Register,
		MethodPolicies: api.MethodPolicies(),
		MCPExtensions:  tools.Register,
		SetupWithManager: func(m manager.Manager) error {
			if err := m.Add(approval.Redeliver{Service: approvals, Interval: cfg.RedeliverInterval.D()}); err != nil {
				return err
			}
			if bq := cfg.Audit.BigQuery; bq != nil {
				return m.Add(&audit.BigQueryExporter{Store: db, Client: googleClient(), Config: *bq, Log: log})
			}
			return nil
		},
	}

	group, ctx := errgroup.WithContext(ctx)
	group.Go(func() error { return app.Run(ctx, kcfg, opts) })
	group.Go(func() error { return serveHooks(ctx, cfg.Hooks.BindAddress, hookHandler.ServeMux()) })
	return group.Wait()
}

func serveHooks(ctx context.Context, addr string, h http.Handler) error {
	srv := &http.Server{Addr: addr, Handler: h, ReadHeaderTimeout: 10 * time.Second}
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdown)
	}()
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("hooks listener: %w", err)
	}
	return nil
}

func loopback(addr string) string {
	host, port, err := net.SplitHostPort(addr)
	if err != nil || host == "" || host == "0.0.0.0" || host == "::" {
		return "127.0.0.1:" + port
	}
	return addr
}

func readSecret(path string) ([]byte, error) {
	s, err := kconfig.ReadSecret(path)
	if err != nil {
		return nil, err
	}
	return []byte(strings.TrimSpace(s)), nil
}

// smtpAuth uses SMTP_USERNAME and SMTP_PASSWORD from the environment, where
// a Kubernetes Secret puts them, or no authentication (an IP-allowlisted relay).
func smtpAuth(addr string) smtp.Auth {
	user, pass := os.Getenv("SMTP_USERNAME"), os.Getenv("SMTP_PASSWORD")
	if user == "" {
		return nil
	}
	host, _, _ := net.SplitHostPort(addr)
	return smtp.PlainAuth("", user, pass, host)
}

// links adapts the store to the authenticator. Sign-ins are recorded at most
// once every five minutes per person, so a busy client does not write on
// every request.
type links struct {
	store *store.Store
	mu    sync.Mutex
	seen  map[string]time.Time
}

func (l *links) ResolveLink(ctx context.Context, issuer, subject string) (string, error) {
	return l.store.ResolveLink(ctx, issuer, subject)
}

func (l *links) PersonGroups(ctx context.Context, id string) (string, string, []string, error) {
	p, err := l.store.GetPerson(ctx, id)
	return p.Email, p.DisplayName, p.Groups, err
}

func (l *links) Seen(ctx context.Context, id, email, name string, groups []string) {
	l.mu.Lock()
	last, ok := l.seen[id]
	if ok && time.Since(last) < 5*time.Minute {
		l.mu.Unlock()
		return
	}
	l.seen[id] = time.Now()
	l.mu.Unlock()
	if err := l.store.UpsertPerson(ctx, store.Person{ID: id, Email: email, DisplayName: name, Groups: groups}); err != nil {
		slog.Default().WarnContext(ctx, "record sign-in", "error", err)
	}
}
