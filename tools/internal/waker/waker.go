// Package waker pauses the cluster's workload node pool when Workbench has
// had no traffic for a while, and starts it again on the next request.
//
// It runs as a sidecar of the web app's nginx, on a small always-on node
// pool. nginx tells it about every API request (activity) and sends it API
// requests the server cannot answer because its nodes are paused (wake).
// Pausing and starting is the GKE node pool's size: zero, or Nodes. Pods
// stay defined and are rescheduled when the nodes come back; PostgreSQL's
// data is on a persistent disk.
package waker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/thehaymaker/kagent-porcelain/tools/internal/httpx"
)

// Config is the waker's configuration.
type Config struct {
	Project, Location, Cluster, Pool string
	Nodes                            int           // nodes when running
	Idle                             time.Duration // pause after this long without traffic
	Debounce                         time.Duration // at most one resize request per this
	// ForceDrainAfter: if the pool still has nodes this long after it was
	// set to 0, delete the pods left on them. GKE's drain honours
	// disruption budgets for up to an hour, and a budget whose pods have
	// nowhere else to go (only this pool runs them) never allows it.
	ForceDrainAfter time.Duration
	ContainerURL    string // default https://container.googleapis.com
	KubeURL         string // default https://kubernetes.default.svc
	KubeToken       func() (string, error)
}

// Validate checks the configuration and fills defaults.
func (c *Config) Validate() error {
	if c.Project == "" || c.Location == "" || c.Cluster == "" || c.Pool == "" {
		return errors.New("waker: project, location, cluster and pool are required")
	}
	if c.Nodes < 1 {
		return errors.New("waker: nodes must be at least 1")
	}
	if c.Idle < time.Minute {
		return errors.New("waker: idle must be at least a minute")
	}
	if c.Debounce == 0 {
		c.Debounce = 2 * time.Minute
	}
	if c.ForceDrainAfter == 0 {
		c.ForceDrainAfter = 2 * time.Minute
	}
	if c.ContainerURL == "" {
		c.ContainerURL = "https://container.googleapis.com"
	}
	if c.KubeURL == "" {
		c.KubeURL = "https://kubernetes.default.svc"
	}
	c.ContainerURL = strings.TrimRight(c.ContainerURL, "/")
	c.KubeURL = strings.TrimRight(c.KubeURL, "/")
	return nil
}

// Waker tracks activity and resizes the pool.
type Waker struct {
	cfg  Config
	gcp  httpx.Doer // Google-authenticated
	kube httpx.Doer // in-cluster API server
	log  *slog.Logger
	now  func() time.Time

	mu         sync.Mutex
	last       time.Time // last activity
	lastResize time.Time
	target     int       // the size last asked for, -1 if unknown
	pausedAt   time.Time // when GKE accepted size 0; zero when not pausing
}

// New builds a Waker. cfg must have been validated.
func New(cfg Config, gcp, kube httpx.Doer, log *slog.Logger) *Waker {
	return &Waker{cfg: cfg, gcp: gcp, kube: kube, log: log, now: time.Now, last: time.Now(), target: -1}
}

// Activity records a request and starts the pool if it is paused.
func (w *Waker) Activity(ctx context.Context) {
	w.mu.Lock()
	w.last = w.now()
	w.mu.Unlock()
	n, err := w.Nodes(ctx)
	if err != nil {
		w.log.WarnContext(ctx, "count nodes", "error", err)
		return
	}
	w.mu.Lock()
	pausing := !w.pausedAt.IsZero()
	w.mu.Unlock()
	// Paused, or still draining towards a pause: ask for the full size. While
	// GKE finishes the earlier resize this is refused, and retried on the
	// next request.
	if n == 0 || pausing {
		w.resize(ctx, w.cfg.Nodes, "a request arrived while paused")
	}
}

// Tick pauses the pool once there has been no activity for Idle.
func (w *Waker) Tick(ctx context.Context) {
	w.mu.Lock()
	idle := w.now().Sub(w.last)
	w.mu.Unlock()
	if idle < w.cfg.Idle {
		return
	}
	nodes, err := w.nodeNames(ctx)
	if err != nil {
		w.log.WarnContext(ctx, "count nodes", "error", err)
		return
	}
	if len(nodes) == 0 {
		w.mu.Lock()
		w.pausedAt = time.Time{}
		w.mu.Unlock()
		return
	}
	w.mu.Lock()
	pausedAt := w.pausedAt
	w.mu.Unlock()
	if pausedAt.IsZero() {
		w.resize(ctx, 0, fmt.Sprintf("no traffic for %s", idle.Round(time.Minute)))
		return
	}
	if w.now().Sub(pausedAt) >= w.cfg.ForceDrainAfter {
		w.forceDrain(ctx, nodes)
	}
}

// forceDrain deletes the pods still on the pool's nodes, except DaemonSet
// and static pods. Deleting, unlike evicting, is not held up by disruption
// budgets; the pods' controllers recreate them, Pending until the pool
// returns.
func (w *Waker) forceDrain(ctx context.Context, nodes []string) {
	deleted := 0
	for _, node := range nodes {
		q := url.Values{"fieldSelector": {"spec.nodeName=" + node}}
		var list struct {
			Items []struct {
				Metadata struct {
					Name            string            `json:"name"`
					Namespace       string            `json:"namespace"`
					Annotations     map[string]string `json:"annotations"`
					OwnerReferences []struct {
						Kind string `json:"kind"`
					} `json:"ownerReferences"`
				} `json:"metadata"`
			} `json:"items"`
		}
		if err := w.kubeJSON(ctx, http.MethodGet, "/api/v1/pods?"+q.Encode(), &list); err != nil {
			w.log.WarnContext(ctx, "list pods to drain", "node", node, "error", err)
			continue
		}
		for _, p := range list.Items {
			if _, static := p.Metadata.Annotations["kubernetes.io/config.mirror"]; static {
				continue
			}
			daemon := false
			for _, o := range p.Metadata.OwnerReferences {
				daemon = daemon || o.Kind == "DaemonSet"
			}
			if daemon {
				continue
			}
			path := "/api/v1/namespaces/" + url.PathEscape(p.Metadata.Namespace) + "/pods/" + url.PathEscape(p.Metadata.Name)
			if err := w.kubeJSON(ctx, http.MethodDelete, path, nil); err != nil {
				w.log.WarnContext(ctx, "delete pod to drain", "pod", p.Metadata.Namespace+"/"+p.Metadata.Name, "error", err)
				continue
			}
			deleted++
		}
	}
	if deleted > 0 {
		w.log.InfoContext(ctx, "drained pods that held up the pause", "pool", w.cfg.Pool, "pods", deleted)
	}
}

// resize asks GKE for size nodes, at most once per Debounce for the same size.
func (w *Waker) resize(ctx context.Context, size int, why string) {
	w.mu.Lock()
	if w.target == size && w.now().Sub(w.lastResize) < w.cfg.Debounce {
		w.mu.Unlock()
		return
	}
	w.target, w.lastResize = size, w.now()
	w.mu.Unlock()
	u := fmt.Sprintf("%s/v1/projects/%s/locations/%s/clusters/%s/nodePools/%s:setSize", w.cfg.ContainerURL,
		url.PathEscape(w.cfg.Project), url.PathEscape(w.cfg.Location), url.PathEscape(w.cfg.Cluster), url.PathEscape(w.cfg.Pool))
	_, err := httpx.Do(ctx, w.gcp, httpx.Request{Method: http.MethodPost, URL: u, Body: map[string]any{"nodeCount": size}}, nil)
	if err != nil {
		// Another operation on the cluster (often the previous resize) blocks
		// this one; the next request or tick tries again.
		w.mu.Lock()
		w.target = -1
		w.mu.Unlock()
		if strings.Contains(err.Error(), "incompatible operation") {
			// GKE runs one operation at a time; the earlier resize (draining
			// nodes) is still going. Not an error: try again later.
			w.log.InfoContext(ctx, "resize waits for a running cluster operation", "pool", w.cfg.Pool, "size", size)
			return
		}
		w.log.WarnContext(ctx, "resize node pool", "pool", w.cfg.Pool, "size", size, "error", err)
		return
	}
	w.mu.Lock()
	if size == 0 {
		w.pausedAt = w.now()
	} else {
		w.pausedAt = time.Time{}
	}
	w.mu.Unlock()
	w.log.InfoContext(ctx, "resized node pool", "pool", w.cfg.Pool, "size", size, "why", why)
}

// Nodes counts the pool's nodes that Kubernetes knows about.
func (w *Waker) Nodes(ctx context.Context) (int, error) {
	names, err := w.nodeNames(ctx)
	return len(names), err
}

func (w *Waker) nodeNames(ctx context.Context) ([]string, error) {
	q := url.Values{"labelSelector": {"cloud.google.com/gke-nodepool=" + w.cfg.Pool}}
	var list struct {
		Items []struct {
			Metadata struct {
				Name string `json:"name"`
			} `json:"metadata"`
		} `json:"items"`
	}
	if err := w.kubeJSON(ctx, http.MethodGet, "/api/v1/nodes?"+q.Encode(), &list); err != nil {
		return nil, fmt.Errorf("listing nodes: %w", err)
	}
	names := make([]string, 0, len(list.Items))
	for _, n := range list.Items {
		names = append(names, n.Metadata.Name)
	}
	return names, nil
}

// kubeJSON calls the Kubernetes API with the pod's service account token.
func (w *Waker) kubeJSON(ctx context.Context, method, path string, out any) error {
	req, err := http.NewRequestWithContext(ctx, method, w.cfg.KubeURL+path, nil)
	if err != nil {
		return err
	}
	if w.cfg.KubeToken != nil {
		tok, err := w.cfg.KubeToken()
		if err != nil {
			return err
		}
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	resp, err := w.kube.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		if resp.StatusCode == http.StatusNotFound && method == http.MethodDelete {
			return nil // already gone
		}
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	if out == nil {
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

// Handler serves the endpoints nginx calls:
//
//	/activity  every API request (a mirror): record it, start the pool if paused
//	/starting  an API request the server could not answer: start the pool if
//	           paused, and answer 503 with a message the web app shows
//	/healthz
func (w *Waker) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/activity", func(rw http.ResponseWriter, r *http.Request) {
		w.Activity(r.Context())
		rw.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("/starting", func(rw http.ResponseWriter, r *http.Request) {
		w.Activity(r.Context())
		n, _ := w.Nodes(r.Context())
		msg := "Workbench is starting up. This takes about 3 to 5 minutes; this page will continue on its own."
		if n > 0 {
			msg = "Workbench is starting up and will be ready in a moment."
		}
		rw.Header().Set("Content-Type", "application/json")
		rw.Header().Set("Retry-After", "20")
		rw.Header().Set("X-Workbench-Starting", "1")
		rw.WriteHeader(http.StatusServiceUnavailable)
		_ = json.NewEncoder(rw).Encode(map[string]string{"code": "unavailable", "message": msg})
	})
	mux.HandleFunc("/healthz", func(rw http.ResponseWriter, _ *http.Request) { _, _ = rw.Write([]byte("ok\n")) })
	return mux
}

// Run ticks every interval until ctx ends.
func (w *Waker) Run(ctx context.Context, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			w.Tick(ctx)
		}
	}
}
