// Command waker pauses Workbench's workload node pool after an idle hour and
// starts it again on the next request (package waker). It runs beside nginx
// in the web pod, on the small always-on node pool, and listens on loopback
// only: nginx is its only client.
package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"flag"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/thehaymaker/kagent-porcelain/tools/internal/app"
	"github.com/thehaymaker/kagent-porcelain/tools/internal/config"
	"github.com/thehaymaker/kagent-porcelain/tools/internal/gcpauth"
	"github.com/thehaymaker/kagent-porcelain/tools/internal/waker"
)

const (
	name   = "waker"
	saDir  = "/var/run/secrets/kubernetes.io/serviceaccount/"
	kubeCA = saDir + "ca.crt"
)

func main() {
	fs := flag.NewFlagSet(name, flag.ExitOnError)
	listen := config.String(fs, "listen", "WAKER_LISTEN", "127.0.0.1:8081", "address nginx calls")
	project := config.String(fs, "project", "WAKER_PROJECT", "", "Google Cloud project of the cluster")
	location := config.String(fs, "location", "WAKER_LOCATION", "", "cluster zone or region")
	cluster := config.String(fs, "cluster", "WAKER_CLUSTER", "", "cluster name")
	pool := config.String(fs, "pool", "WAKER_POOL", "workload", "node pool to pause and start")
	nodes := config.Int64(fs, "nodes", "WAKER_NODES", 2, "nodes when running")
	idle := config.Duration(fs, "idle", "WAKER_IDLE", time.Hour, "pause after this long without traffic")
	_ = fs.Parse(os.Args[1:])
	log := slog.New(slog.NewJSONHandler(os.Stdout, nil)).With("service", name)

	cfg := waker.Config{
		Project: *project, Location: *location, Cluster: *cluster, Pool: *pool, Nodes: int(*nodes), Idle: *idle,
		KubeToken: func() (string, error) {
			b, err := os.ReadFile(saDir + "token") // rotated by the kubelet; read each time
			return strings.TrimSpace(string(b)), err
		},
	}
	if err := cfg.Validate(); err != nil {
		app.Fatal(name, err)
	}
	gcp, err := gcpauth.Client(context.Background())
	if err != nil {
		app.Fatal(name, err)
	}
	ca, err := os.ReadFile(kubeCA)
	if err != nil {
		app.Fatal(name, err)
	}
	pool509 := x509.NewCertPool()
	if !pool509.AppendCertsFromPEM(ca) {
		app.Fatal(name, errors.New("no certificates in "+kubeCA))
	}
	kube := &http.Client{Timeout: 10 * time.Second, Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool509, MinVersion: tls.VersionTLS12}}}

	w := waker.New(cfg, gcp, kube, log)
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()
	go w.Run(ctx, time.Minute)
	srv := &http.Server{Addr: *listen, Handler: w.Handler(), ReadHeaderTimeout: 5 * time.Second}
	go func() {
		<-ctx.Done()
		_ = srv.Shutdown(context.Background())
	}()
	log.Info("waker started", "pool", cfg.Pool, "nodes", cfg.Nodes, "idle", cfg.Idle.String())
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		app.Fatal(name, err)
	}
}
