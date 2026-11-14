// Package gcpauth builds the Google-authenticated HTTP client. In-cluster it
// resolves Workload Identity through Application Default Credentials; nothing
// is read from tool arguments and tokens never leave the transport.
package gcpauth

import (
	"context"
	"net/http"
	"time"

	"golang.org/x/oauth2/google"
)

// CloudPlatformScope is the broad scope; IAM on the Workload Identity service
// account is what actually limits access.
const CloudPlatformScope = "https://www.googleapis.com/auth/cloud-platform"

// Client returns an *http.Client that attaches ADC access tokens.
func Client(ctx context.Context, scopes ...string) (*http.Client, error) {
	if len(scopes) == 0 {
		scopes = []string{CloudPlatformScope}
	}
	c, err := google.DefaultClient(ctx, scopes...)
	if err != nil {
		return nil, err
	}
	c.Timeout = 60 * time.Second
	return c, nil
}
