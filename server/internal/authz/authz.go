// Package authz decides what a signed-in person may do in kagent, from their
// company directory groups. It implements kagent's CollectionAuthorizer, so
// every kagent service consults it, and Workbench's own services use the
// same roles.
package authz

import (
	"context"
	"fmt"
	"slices"

	"github.com/kagent-dev/kagent/go/api/authorization"
	"github.com/kagent-dev/kagent/go/core/pkg/auth"

	"github.com/thehaymaker/kagent-porcelain/server/internal/identity"
)

// Config maps directory groups to roles.
type Config struct {
	// PlatformGroups may do anything, including define agents, models and tools.
	PlatformGroups []string `json:"platformGroups" yaml:"platformGroups"`
	// RequesterGroups may use agents. Empty means every signed-in person.
	RequesterGroups []string `json:"requesterGroups" yaml:"requesterGroups"`
	// AuditorGroups may read every conversation and the whole audit trail.
	AuditorGroups []string `json:"auditorGroups" yaml:"auditorGroups"`
	// AgentNamespaces hold the agents requesters may see and talk to.
	AgentNamespaces []string `json:"agentNamespaces" yaml:"agentNamespaces"`
}

// Role is a Workbench role.
type Role string

const (
	Requester        Role = "requester"
	Approver         Role = "approver"
	PlatformEngineer Role = "platform"
	Auditor          Role = "auditor"
)

// Authorizer implements auth.CollectionAuthorizer.
type Authorizer struct {
	cfg Config
	// approverGroups come from the approval policy, so the two cannot drift.
	approverGroups []string
}

var _ auth.CollectionAuthorizer = (*Authorizer)(nil)

// New returns an authorizer.
func New(cfg Config, approverGroups []string) (*Authorizer, error) {
	if len(cfg.PlatformGroups) == 0 {
		return nil, fmt.Errorf("authz: at least one platform group is required")
	}
	if len(cfg.AgentNamespaces) == 0 {
		return nil, fmt.Errorf("authz: at least one agent namespace is required")
	}
	return &Authorizer{cfg: cfg, approverGroups: approverGroups}, nil
}

// Roles returns a caller's roles.
func (a *Authorizer) Roles(groups []string) []Role {
	var roles []Role
	in := func(set []string) bool {
		return slices.ContainsFunc(groups, func(g string) bool { return slices.Contains(set, g) })
	}
	if len(a.cfg.RequesterGroups) == 0 || in(a.cfg.RequesterGroups) || in(a.cfg.PlatformGroups) {
		roles = append(roles, Requester)
	}
	if in(a.approverGroups) {
		roles = append(roles, Approver)
	}
	if in(a.cfg.PlatformGroups) {
		roles = append(roles, PlatformEngineer)
	}
	if in(a.cfg.AuditorGroups) || in(a.cfg.PlatformGroups) {
		roles = append(roles, Auditor)
	}
	return roles
}

// Has reports whether groups grant a role.
func (a *Authorizer) Has(groups []string, r Role) bool { return slices.Contains(a.Roles(groups), r) }

// requesterAccess lists what requesters may do with kagent's resource types.
// Everything not listed is for platform engineers only.
var requesterAccess = map[string][]auth.Verb{
	"Agent":        {auth.VerbGet, auth.VerbList},
	"Session":      {auth.VerbGet, auth.VerbList, auth.VerbCreate, auth.VerbUpdate, auth.VerbDelete},
	"SessionShare": {auth.VerbGet, auth.VerbList, auth.VerbCreate, auth.VerbDelete},
	"Checkpoint":   {auth.VerbGet, auth.VerbList, auth.VerbCreate, auth.VerbUpdate, auth.VerbDelete},
	"ScheduledRun": {auth.VerbGet, auth.VerbList, auth.VerbCreate, auth.VerbUpdate, auth.VerbDelete},
	"Memory":       {auth.VerbGet, auth.VerbList},
	"ModelConfig":  {auth.VerbGet, auth.VerbList},
}

// auditorAccess adds read access across creators for auditors.
var auditorAccess = map[string][]auth.Verb{
	"SessionAllCreators": {auth.VerbList, auth.VerbGet},
}

// namespaced types are limited to AgentNamespaces for requesters.
var namespaced = map[string]bool{"Agent": true, "Session": true, "ScheduledRun": true}

// Check decides one action.
func (a *Authorizer) Check(_ context.Context, p auth.Principal, verb auth.Verb, r auth.Resource) error {
	if p.ID == "" {
		return deny(verb, r)
	}
	groups := identity.FromPrincipal(p).Groups
	roles := a.Roles(groups)
	if slices.Contains(roles, PlatformEngineer) {
		return nil
	}
	if slices.Contains(roles, Auditor) && slices.Contains(auditorAccess[r.Type], verb) {
		return nil
	}
	if !slices.Contains(roles, Requester) || !slices.Contains(requesterAccess[r.Type], verb) {
		return deny(verb, r)
	}
	if namespaced[r.Type] && r.Namespace != "" && !slices.Contains(a.cfg.AgentNamespaces, r.Namespace) {
		return deny(verb, r)
	}
	return nil
}

// Scope narrows a listing.
func (a *Authorizer) Scope(_ context.Context, p auth.Principal, verb auth.Verb, resourceType string) (authorization.AuthorizationScope, error) {
	none := authorization.AuthorizationScope{Kind: authorization.ScopeNone}
	if p.ID == "" {
		return none, nil
	}
	roles := a.Roles(identity.FromPrincipal(p).Groups)
	if slices.Contains(roles, PlatformEngineer) {
		return authorization.AuthorizationScope{Kind: authorization.ScopeAll}, nil
	}
	if !slices.Contains(roles, Requester) || !slices.Contains(requesterAccess[resourceType], verb) {
		return none, nil
	}
	if !namespaced[resourceType] {
		return authorization.AuthorizationScope{Kind: authorization.ScopeAll}, nil
	}
	return authorization.AuthorizationScope{Kind: authorization.ScopeAnyOf, AnyOf: []authorization.ScopeClause{{
		All: []authorization.ScopePredicate{{Attribute: authorization.AttributeNamespace, Operator: authorization.ScopeIn, Values: slices.Clone(a.cfg.AgentNamespaces)}},
	}}}, nil
}

// PermissionDeniedError is returned by Check.
type PermissionDeniedError struct {
	Verb     auth.Verb
	Resource auth.Resource
}

func (e *PermissionDeniedError) Error() string {
	return fmt.Sprintf("permission denied: %s %s", e.Verb, e.Resource.Type)
}

func deny(v auth.Verb, r auth.Resource) error { return &PermissionDeniedError{Verb: v, Resource: r} }
