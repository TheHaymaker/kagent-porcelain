// Package identity is Workbench's authenticator. It replaces kagent's
// cookie-and-proxy sign-in with bearer tokens that every surface can carry:
// the web app and mobile apps hold a company OIDC token, the Teams bot holds
// a Microsoft token that resolves to a company person through a link, and
// the server itself mints short-lived delegation tokens for its own loopback
// calls into kagent.
package identity

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/kagent-dev/kagent/go/core/pkg/auth"
	"github.com/lestrrat-go/jwx/jwk"
	"github.com/lestrrat-go/jwx/jwt"
)

// ErrUnauthenticated is returned for any credential that does not identify a person.
var ErrUnauthenticated = errors.New("unauthenticated")

// ErrNotLinked is returned for a valid token from a linked-identity issuer
// that no person has linked yet.
var ErrNotLinked = errors.New("identity not linked to a company account")

// IssuerKind says what a token from an issuer means.
type IssuerKind string

const (
	// Company issuers sign people in directly. Their subjects are principals.
	Company IssuerKind = "company"
	// Linked issuers (Microsoft, for Teams) only identify a person through an identity link.
	Linked IssuerKind = "linked"
)

// Issuer configures one trusted token issuer.
type Issuer struct {
	Kind      IssuerKind `json:"kind" yaml:"kind"`
	URL       string     `json:"url" yaml:"url"`
	Audiences []string   `json:"audiences" yaml:"audiences"`
	// JWKSURL overrides discovery.
	JWKSURL     string `json:"jwksUrl,omitempty" yaml:"jwksUrl,omitempty"`
	EmailClaim  string `json:"emailClaim,omitempty" yaml:"emailClaim,omitempty"`
	NameClaim   string `json:"nameClaim,omitempty" yaml:"nameClaim,omitempty"`
	GroupsClaim string `json:"groupsClaim,omitempty" yaml:"groupsClaim,omitempty"`
	// SubjectClaim names the stable subject; Microsoft's is "oid".
	SubjectClaim string `json:"subjectClaim,omitempty" yaml:"subjectClaim,omitempty"`
	// Surface labels sessions from this issuer (web, teams, ios, android).
	// A "surface" claim or X-Workbench-Surface header overrides it.
	Surface string `json:"surface,omitempty" yaml:"surface,omitempty"`
}

// Claims Workbench adds to a principal.
const (
	ClaimEmail      = "email"
	ClaimName       = "name"
	ClaimGroups     = "groups"
	ClaimSurface    = "workbench_surface"
	ClaimActor      = "workbench_actor"
	ClaimDelegated  = "workbench_delegated"
	InternalIssuer  = "workbench-internal"
	SurfaceHeader   = "X-Workbench-Surface"
	internalTTL     = 2 * time.Minute
	maxClockSkew    = 60 * time.Second
	defaultSurface  = "web"
	tokenTypeHeader = "wbi"
	// SystemPrincipal is the server itself, for reads no person makes (looking
	// up the session a push notification is about). It holds SystemGroups.
	SystemPrincipal = "workbench-system"
)

// Links resolves linked identities and remembers who signed in.
type Links interface {
	ResolveLink(ctx context.Context, issuer, subject string) (string, error)
	// PersonGroups returns what the person's company sign-in last reported.
	PersonGroups(ctx context.Context, personID string) (email, name string, groups []string, err error)
	// Seen records a company sign-in.
	Seen(ctx context.Context, personID, email, name string, groups []string)
}

// KeySet fetches an issuer's verification keys.
type KeySet interface {
	Keys(ctx context.Context, issuer Issuer) (jwk.Set, error)
}

// Authenticator implements kagent's auth.AuthProvider.
type Authenticator struct {
	issuers  []Issuer
	keys     KeySet
	links    Links
	internal []byte
	// SystemGroups are the groups SystemPrincipal holds; typically the
	// platform group, so it may read any session.
	SystemGroups []string
	now          func() time.Time
	log      *slog.Logger
}

var _ auth.AuthProvider = (*Authenticator)(nil)

// New builds an authenticator. internalKey signs the server's own delegation
// tokens and must be at least 32 bytes.
func New(issuers []Issuer, keys KeySet, links Links, internalKey []byte) (*Authenticator, error) {
	if len(internalKey) < 32 {
		return nil, fmt.Errorf("identity: internal signing key must be at least 32 bytes")
	}
	if len(issuers) == 0 {
		return nil, fmt.Errorf("identity: at least one issuer is required")
	}
	for i := range issuers {
		is := &issuers[i]
		is.URL = strings.TrimRight(is.URL, "/")
		if is.URL == "" || len(is.Audiences) == 0 {
			return nil, fmt.Errorf("identity: issuer %d needs a url and at least one audience", i)
		}
		if is.Kind != Company && is.Kind != Linked {
			return nil, fmt.Errorf("identity: issuer %s has unknown kind %q", is.URL, is.Kind)
		}
		if is.URL == InternalIssuer {
			return nil, fmt.Errorf("identity: %s is reserved", InternalIssuer)
		}
	}
	return &Authenticator{issuers: issuers, keys: keys, links: links, internal: internalKey, now: time.Now, log: slog.Default()}, nil
}

// session is an authenticated caller. It deliberately carries no token
// source, so kagent never forwards the caller's own credential onwards.
type session struct{ p auth.Principal }

func (s session) Principal() auth.Principal { return s.p }

// Authenticate identifies the caller from its bearer token.
func (a *Authenticator) Authenticate(ctx context.Context, h http.Header, _ url.Values) (auth.Session, error) {
	if len(h.Values("Authorization")) != 1 {
		return nil, ErrUnauthenticated
	}
	raw, ok := strings.CutPrefix(h.Get("Authorization"), "Bearer ")
	if !ok || raw == "" {
		return nil, ErrUnauthenticated
	}
	if strings.HasPrefix(raw, tokenTypeHeader+".") {
		p, err := a.verifyInternal(raw)
		if err != nil {
			return nil, ErrUnauthenticated
		}
		return session{p}, nil
	}
	p, err := a.verifyExternal(ctx, raw, h.Get(SurfaceHeader))
	if err != nil {
		a.log.DebugContext(ctx, "token rejected", "error", err)
		if errors.Is(err, ErrNotLinked) {
			return nil, err
		}
		return nil, ErrUnauthenticated
	}
	return session{p}, nil
}

// UpstreamAuth passes the caller's identity, not their token, onwards.
func (a *Authenticator) UpstreamAuth(r *http.Request, s auth.Session, _ string) error {
	return auth.ForwardCredential(r, s)
}

func (a *Authenticator) issuer(iss string) (Issuer, bool) {
	iss = strings.TrimRight(iss, "/")
	for _, is := range a.issuers {
		if is.URL == iss {
			return is, true
		}
	}
	return Issuer{}, false
}

func (a *Authenticator) verifyExternal(ctx context.Context, raw, surfaceHeader string) (auth.Principal, error) {
	unverified, err := jwt.ParseString(raw)
	if err != nil {
		return auth.Principal{}, err
	}
	is, ok := a.issuer(unverified.Issuer())
	if !ok {
		return auth.Principal{}, fmt.Errorf("untrusted issuer %q", unverified.Issuer())
	}
	set, err := a.keys.Keys(ctx, is)
	if err != nil {
		return auth.Principal{}, fmt.Errorf("keys for %s: %w", is.URL, err)
	}
	tok, err := jwt.ParseString(raw, jwt.WithKeySet(set), jwt.WithValidate(true),
		jwt.WithClock(jwt.ClockFunc(a.now)), jwt.WithAcceptableSkew(maxClockSkew))
	if err != nil {
		return auth.Principal{}, err
	}
	if strings.TrimRight(tok.Issuer(), "/") != is.URL {
		return auth.Principal{}, fmt.Errorf("issuer mismatch")
	}
	if !slices.ContainsFunc(tok.Audience(), func(aud string) bool { return slices.Contains(is.Audiences, aud) }) {
		return auth.Principal{}, fmt.Errorf("audience not accepted")
	}
	if tok.Expiration().IsZero() {
		return auth.Principal{}, fmt.Errorf("token has no expiry")
	}
	claims, err := tok.AsMap(ctx)
	if err != nil {
		return auth.Principal{}, err
	}
	subject := tok.Subject()
	if is.SubjectClaim != "" {
		subject, _ = claims[is.SubjectClaim].(string)
	}
	if subject == "" {
		return auth.Principal{}, fmt.Errorf("token has no subject")
	}
	surface := firstNonEmpty(stringClaim(claims, "surface"), surfaceHeader, is.Surface, defaultSurface)

	if is.Kind == Linked {
		personID, err := a.links.ResolveLink(ctx, is.URL, subject)
		if err != nil {
			return auth.Principal{}, fmt.Errorf("%w (%s)", ErrNotLinked, err)
		}
		email, name, groups, err := a.links.PersonGroups(ctx, personID)
		if err != nil {
			return auth.Principal{}, err
		}
		return auth.Principal{ID: personID, Claims: map[string]any{
			"iss": is.URL, "sub": subject, ClaimEmail: email, ClaimName: name, ClaimGroups: groups, ClaimSurface: surface,
		}}, nil
	}

	email := stringClaim(claims, firstNonEmpty(is.EmailClaim, "email"))
	name := stringClaim(claims, firstNonEmpty(is.NameClaim, "name"))
	groups := stringsClaim(claims, firstNonEmpty(is.GroupsClaim, "groups"))
	id := auth.PrincipalID(is.URL, subject)
	if a.links != nil {
		a.links.Seen(ctx, id, email, name, groups)
	}
	return auth.Principal{ID: id, Claims: map[string]any{
		"iss": is.URL, "sub": subject, ClaimEmail: email, ClaimName: name, ClaimGroups: groups, ClaimSurface: surface,
	}}, nil
}

// VerifyLinkToken checks an ID token from a linked issuer for the
// LinkIdentity call and returns its issuer, subject and a display hint.
func (a *Authenticator) VerifyLinkToken(ctx context.Context, raw string) (issuer, subject, hint string, err error) {
	unverified, err := jwt.ParseString(raw)
	if err != nil {
		return "", "", "", ErrUnauthenticated
	}
	is, ok := a.issuer(unverified.Issuer())
	if !ok || is.Kind != Linked {
		return "", "", "", fmt.Errorf("issuer %q is not one that can be linked", unverified.Issuer())
	}
	set, err := a.keys.Keys(ctx, is)
	if err != nil {
		return "", "", "", err
	}
	tok, err := jwt.ParseString(raw, jwt.WithKeySet(set), jwt.WithValidate(true),
		jwt.WithClock(jwt.ClockFunc(a.now)), jwt.WithAcceptableSkew(maxClockSkew))
	if err != nil {
		return "", "", "", ErrUnauthenticated
	}
	if !slices.ContainsFunc(tok.Audience(), func(aud string) bool { return slices.Contains(is.Audiences, aud) }) {
		return "", "", "", ErrUnauthenticated
	}
	claims, err := tok.AsMap(ctx)
	if err != nil {
		return "", "", "", err
	}
	subject = tok.Subject()
	if is.SubjectClaim != "" {
		subject, _ = claims[is.SubjectClaim].(string)
	}
	if subject == "" {
		return "", "", "", ErrUnauthenticated
	}
	hint = firstNonEmpty(stringClaim(claims, "preferred_username"), stringClaim(claims, "email"), stringClaim(claims, "name"))
	return is.URL, subject, hint, nil
}

// internalClaims are the payload of a delegation token.
type internalClaims struct {
	Sub     string   `json:"sub"`
	Actor   string   `json:"act,omitempty"`
	Groups  []string `json:"groups,omitempty"`
	Email   string   `json:"email,omitempty"`
	Expires int64    `json:"exp"`
	Purpose string   `json:"purpose"`
}

// Mint issues a short-lived token the server uses to call kagent as
// principalID. actorID records who the call is really for (an approver whose
// decision is being delivered); it is kept on the principal for audit.
//
// These tokens never leave the process's loopback calls: nothing returns
// them to a client.
func (a *Authenticator) Mint(principalID, actorID, purpose string) string {
	c := internalClaims{Sub: principalID, Actor: actorID, Expires: a.now().Add(internalTTL).Unix(), Purpose: purpose}
	body, _ := json.Marshal(c)
	payload := base64.RawURLEncoding.EncodeToString(body)
	return tokenTypeHeader + "." + payload + "." + a.sign(payload)
}

func (a *Authenticator) sign(payload string) string {
	m := hmac.New(sha256.New, a.internal)
	m.Write([]byte(tokenTypeHeader + "." + payload))
	return base64.RawURLEncoding.EncodeToString(m.Sum(nil))
}

func (a *Authenticator) verifyInternal(raw string) (auth.Principal, error) {
	parts := strings.Split(raw, ".")
	if len(parts) != 3 || parts[0] != tokenTypeHeader {
		return auth.Principal{}, ErrUnauthenticated
	}
	if !hmac.Equal([]byte(a.sign(parts[1])), []byte(parts[2])) {
		return auth.Principal{}, ErrUnauthenticated
	}
	body, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return auth.Principal{}, ErrUnauthenticated
	}
	var c internalClaims
	if err := json.Unmarshal(body, &c); err != nil || c.Sub == "" {
		return auth.Principal{}, ErrUnauthenticated
	}
	if a.now().Unix() > c.Expires {
		return auth.Principal{}, ErrUnauthenticated
	}
	claims := map[string]any{"iss": InternalIssuer, ClaimDelegated: true, ClaimSurface: "workbench", "purpose": c.Purpose}
	if c.Actor != "" {
		claims[ClaimActor] = c.Actor
	}
	if c.Sub == SystemPrincipal {
		claims[ClaimGroups] = slices.Clone(a.SystemGroups)
		claims[ClaimName] = "Workbench"
	} else if a.links != nil {
		if email, name, groups, err := a.links.PersonGroups(context.Background(), c.Sub); err == nil {
			claims[ClaimEmail], claims[ClaimName], claims[ClaimGroups] = email, name, groups
		}
	}
	return auth.Principal{ID: c.Sub, Claims: claims}, nil
}

// Caller is the identity of a request as Workbench's own services see it.
type Caller struct {
	ID      string
	Email   string
	Name    string
	Groups  []string
	Surface string
	// Delegated is true for the server's own loopback calls.
	Delegated bool
	// RuntimeSessionID is set when an agent runtime is calling (kagent's
	// runtime binding): the session it runs, and CreatorID the requester.
	RuntimeSessionID string
	CreatorID        string
}

// IsRuntime reports whether an agent runtime made the call.
func (c Caller) IsRuntime() bool { return c.RuntimeSessionID != "" }

// FromPrincipal reads Workbench's view of a kagent principal.
func FromPrincipal(p auth.Principal) Caller {
	c := Caller{ID: p.ID}
	if p.RuntimeBinding != nil {
		c.RuntimeSessionID = p.RuntimeBinding.SessionID
		c.CreatorID = p.RuntimeBinding.CreatorID
	}
	c.Email = stringClaim(p.Claims, ClaimEmail)
	c.Name = stringClaim(p.Claims, ClaimName)
	c.Groups = stringsClaim(p.Claims, ClaimGroups)
	c.Surface = stringClaim(p.Claims, ClaimSurface)
	c.Delegated, _ = p.Claims[ClaimDelegated].(bool)
	return c
}

// FromContext returns the caller kagent's interceptors authenticated.
func FromContext(ctx context.Context) (Caller, bool) {
	s, ok := auth.AuthSessionFrom(ctx)
	if !ok {
		return Caller{}, false
	}
	c := FromPrincipal(s.Principal())
	return c, c.ID != "" || c.IsRuntime()
}

func stringClaim(m map[string]any, k string) string {
	s, _ := m[k].(string)
	return s
}

func stringsClaim(m map[string]any, k string) []string {
	switch v := m[k].(type) {
	case []string:
		return slices.Clone(v)
	case []any:
		out := make([]string, 0, len(v))
		for _, x := range v {
			if s, ok := x.(string); ok {
				out = append(out, s)
			}
		}
		return out
	case string:
		if v == "" {
			return nil
		}
		return strings.Fields(strings.ReplaceAll(v, ",", " "))
	}
	return nil
}

func firstNonEmpty(v ...string) string {
	for _, s := range v {
		if s != "" {
			return s
		}
	}
	return ""
}
