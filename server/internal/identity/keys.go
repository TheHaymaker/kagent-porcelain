package identity

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/lestrrat-go/jwx/jwk"
)

// DiscoveryKeySet fetches keys through OIDC discovery and keeps them fresh.
type DiscoveryKeySet struct {
	client  *http.Client
	refresh *jwk.AutoRefresh
	mu      sync.Mutex
	urls    map[string]string
}

// NewDiscoveryKeySet returns a key set that lives as long as ctx.
func NewDiscoveryKeySet(ctx context.Context, client *http.Client) *DiscoveryKeySet {
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	return &DiscoveryKeySet{client: client, refresh: jwk.NewAutoRefresh(ctx), urls: map[string]string{}}
}

// Keys returns the issuer's current key set.
func (d *DiscoveryKeySet) Keys(ctx context.Context, is Issuer) (jwk.Set, error) {
	u, err := d.jwksURL(ctx, is)
	if err != nil {
		return nil, err
	}
	return d.refresh.Fetch(ctx, u)
}

func (d *DiscoveryKeySet) jwksURL(ctx context.Context, is Issuer) (string, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if u, ok := d.urls[is.URL]; ok {
		return u, nil
	}
	u := is.JWKSURL
	if u == "" {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, is.URL+"/.well-known/openid-configuration", nil)
		if err != nil {
			return "", err
		}
		resp, err := d.client.Do(req)
		if err != nil {
			return "", fmt.Errorf("discover %s: %w", is.URL, err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return "", fmt.Errorf("discover %s: %s", is.URL, resp.Status)
		}
		var doc struct {
			JWKSURI string `json:"jwks_uri"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&doc); err != nil || doc.JWKSURI == "" {
			return "", fmt.Errorf("discover %s: no jwks_uri", is.URL)
		}
		u = doc.JWKSURI
	}
	d.refresh.Configure(u, jwk.WithHTTPClient(d.client), jwk.WithMinRefreshInterval(5*time.Minute))
	d.urls[is.URL] = u
	return u, nil
}
