// Package config is Workbench's own configuration, kept beside kagent's
// controller configuration rather than inside it, so kagent's file stays
// exactly what kagent documents.
package config

import (
	"fmt"
	"net/url"
	"os"
	"strings"
	"time"

	"sigs.k8s.io/yaml"

	"github.com/thehaymaker/kagent-porcelain/server/internal/audit"
	"github.com/thehaymaker/kagent-porcelain/server/internal/authz"
	"github.com/thehaymaker/kagent-porcelain/server/internal/duration"
	"github.com/thehaymaker/kagent-porcelain/server/internal/identity"
	"github.com/thehaymaker/kagent-porcelain/server/internal/policy"
	"github.com/thehaymaker/kagent-porcelain/server/internal/registry"
)

// Config is the Workbench configuration file.
type Config struct {
	// PublicURL is the web app's address, for links in email and push.
	PublicURL string `json:"publicUrl"`
	// PublicExposure declares the web app reachable from the internet. Test
	// issuers (keys read from a file) are then refused (ADR 0005).
	PublicExposure bool `json:"publicExposure,omitempty"`
	Identity       struct {
		Issuers []identity.Issuer `json:"issuers"`
		// InternalKeyFile holds at least 32 random bytes for delegation tokens.
		InternalKeyFile string `json:"internalKeyFile"`
	} `json:"identity"`
	Authorization authz.Config `json:"authorization"`
	// Approvals is the approval policy. Omitted, the strict default applies
	// with DefaultApproverGroup.
	Approvals            *policy.Policy `json:"approvals,omitempty"`
	DefaultApproverGroup string         `json:"defaultApproverGroup"`
	Hooks                struct {
		// BindAddress is the internal listener for kagent push and gateway usage.
		BindAddress string `json:"bindAddress"`
		// CallbackURL is the push URL kagent should call, as kagent reaches it.
		CallbackURL string `json:"callbackUrl"`
		// PushIssuer is kagent's push.issuer.
		PushIssuer      string `json:"pushIssuer"`
		UsageSecretFile string `json:"usageSecretFile"`
	} `json:"hooks"`
	Production struct {
		// Deployer is "cloudrun" or "log" (development only).
		Deployer string                  `json:"deployer"`
		CloudRun registry.CloudRunConfig `json:"cloudRun"`
	} `json:"production"`
	Audit struct {
		BigQuery *audit.BigQueryConfig `json:"bigQuery,omitempty"`
	} `json:"audit"`
	Notifications struct {
		SMTP *struct {
			Addr string `json:"addr"`
			From string `json:"from"`
		} `json:"smtp,omitempty"`
		Teams *struct {
			URL        string `json:"url"`
			SecretFile string `json:"secretFile"`
		} `json:"teams,omitempty"`
		FCMProject string `json:"fcmProject,omitempty"`
	} `json:"notifications"`
	RedeliverInterval duration.Duration `json:"redeliverInterval"`
}

// Load reads and validates a configuration file.
func Load(path string) (*Config, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var c Config
	if err := yaml.UnmarshalStrict(b, &c); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return &c, c.Validate()
}

// Policy returns the configured or default approval policy.
func (c *Config) Policy() policy.Policy {
	if c.Approvals != nil {
		return *c.Approvals
	}
	return policy.Default(c.DefaultApproverGroup)
}

// ApproverGroups lists every group named by the policy.
func (c *Config) ApproverGroups() []string {
	var out []string
	for _, k := range c.Policy().Kinds {
		out = append(out, k.ApproverGroups...)
	}
	return out
}

// Validate checks the configuration.
func (c *Config) Validate() error {
	var errs []string
	add := func(f string, a ...any) { errs = append(errs, fmt.Sprintf(f, a...)) }
	if len(c.Identity.Issuers) == 0 {
		add("identity.issuers: at least one issuer is required")
	}
	if c.PublicExposure {
		for _, is := range c.Identity.Issuers {
			if is.JWKSFile != "" {
				add("identity.issuers: %s reads its keys from a file, which only test issuers do; remove it or set publicExposure: false", is.URL)
			}
		}
	}
	if c.Identity.InternalKeyFile == "" {
		add("identity.internalKeyFile is required")
	}
	if c.Approvals == nil && c.DefaultApproverGroup == "" {
		add("set approvals or defaultApproverGroup")
	}
	if err := c.Policy().Validate(); err != nil {
		add("%v", err)
	}
	if len(c.Authorization.PlatformGroups) == 0 || len(c.Authorization.AgentNamespaces) == 0 {
		add("authorization.platformGroups and authorization.agentNamespaces are required")
	}
	if c.Hooks.BindAddress == "" {
		c.Hooks.BindAddress = ":8090"
	}
	if c.Hooks.CallbackURL != "" {
		if u, err := url.Parse(c.Hooks.CallbackURL); err != nil || !strings.HasSuffix(u.Path, "/hooks/a2a") {
			add("hooks.callbackUrl must be an absolute URL ending in /hooks/a2a")
		}
		if c.Hooks.PushIssuer == "" {
			add("hooks.pushIssuer is required with hooks.callbackUrl: push notifications are always verified")
		}
	}
	switch c.Production.Deployer {
	case "cloudrun":
		if c.Production.CloudRun.Project == "" || c.Production.CloudRun.Region == "" {
			add("production.cloudRun.project and region are required")
		}
	case "log", "":
		c.Production.Deployer = "log"
	default:
		add("production.deployer must be cloudrun or log")
	}
	if c.RedeliverInterval == 0 {
		c.RedeliverInterval = duration.Duration(5 * time.Second)
	}
	if len(errs) > 0 {
		return fmt.Errorf("workbench config:\n  %s", strings.Join(errs, "\n  "))
	}
	return nil
}
