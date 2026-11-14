package identity

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/lestrrat-go/jwx/jwa"
	"github.com/lestrrat-go/jwx/jwk"
	"github.com/lestrrat-go/jwx/jwt"
)

type staticKeys map[string]jwk.Set

func (s staticKeys) Keys(_ context.Context, is Issuer) (jwk.Set, error) { return s[is.URL], nil }

type fakeLinks struct {
	links map[string]string
	seen  []string
}

func (f *fakeLinks) ResolveLink(_ context.Context, iss, sub string) (string, error) {
	if id, ok := f.links[iss+"|"+sub]; ok {
		return id, nil
	}
	return "", errors.New("no link")
}
func (f *fakeLinks) PersonGroups(context.Context, string) (string, string, []string, error) {
	return "alice@example.com", "Alice", []string{"requesters"}, nil
}
func (f *fakeLinks) Seen(_ context.Context, id, _, _ string, _ []string) { f.seen = append(f.seen, id) }

type signer struct {
	key jwk.Key
	set jwk.Set
}

func newSigner(t *testing.T) signer {
	t.Helper()
	raw, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	key, _ := jwk.New(raw)
	_ = key.Set(jwk.KeyIDKey, "k1")
	_ = key.Set(jwk.AlgorithmKey, jwa.RS256)
	pub, _ := jwk.PublicKeyOf(key)
	set := jwk.NewSet()
	set.Add(pub)
	return signer{key: key, set: set}
}

func (s signer) token(t *testing.T, claims map[string]any) string {
	t.Helper()
	tok := jwt.New()
	for k, v := range claims {
		if err := tok.Set(k, v); err != nil {
			t.Fatal(err)
		}
	}
	b, err := jwt.Sign(tok, jwa.RS256, s.key)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

const (
	company = "https://login.example.com"
	ms      = "https://login.microsoftonline.com/tenant/v2.0"
)

func setup(t *testing.T) (*Authenticator, signer, signer, *fakeLinks) {
	c, m := newSigner(t), newSigner(t)
	links := &fakeLinks{links: map[string]string{ms + "|oid-alice": "alice-principal"}}
	a, err := New([]Issuer{
		{Kind: Company, URL: company + "/", Audiences: []string{"workbench"}},
		{Kind: Linked, URL: ms, Audiences: []string{"api://workbench"}, SubjectClaim: "oid", Surface: "teams"},
	}, staticKeys{company: c.set, ms: m.set}, links, []byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatal(err)
	}
	return a, c, m, links
}

func header(tok string) http.Header {
	h := http.Header{}
	h.Set("Authorization", "Bearer "+tok)
	return h
}

func TestCompanyToken(t *testing.T) {
	a, c, _, links := setup(t)
	tok := c.token(t, map[string]any{"iss": company, "sub": "u1", "aud": "workbench", "exp": time.Now().Add(time.Hour).Unix(),
		"email": "alice@example.com", "groups": []string{"requesters", "approvers"}})
	s, err := a.Authenticate(context.Background(), header(tok), nil)
	if err != nil {
		t.Fatal(err)
	}
	caller := FromPrincipal(s.Principal())
	if caller.Email != "alice@example.com" || len(caller.Groups) != 2 || caller.Surface != "web" {
		t.Fatalf("caller %+v", caller)
	}
	if len(links.seen) != 1 {
		t.Fatal("company sign-in must be recorded")
	}
}

func TestRejects(t *testing.T) {
	a, c, m, _ := setup(t)
	exp := time.Now().Add(time.Hour).Unix()
	cases := map[string]string{
		"wrong audience": c.token(t, map[string]any{"iss": company, "sub": "u", "aud": "other", "exp": exp}),
		"expired":        c.token(t, map[string]any{"iss": company, "sub": "u", "aud": "workbench", "exp": time.Now().Add(-time.Hour).Unix()}),
		"no expiry":      c.token(t, map[string]any{"iss": company, "sub": "u", "aud": "workbench"}),
		"unknown issuer": c.token(t, map[string]any{"iss": "https://evil", "sub": "u", "aud": "workbench", "exp": exp}),
		"wrong key":      m.token(t, map[string]any{"iss": company, "sub": "u", "aud": "workbench", "exp": exp}),
		"no subject":     c.token(t, map[string]any{"iss": company, "aud": "workbench", "exp": exp}),
		"not a jwt":      "garbage",
	}
	for name, tok := range cases {
		if _, err := a.Authenticate(context.Background(), header(tok), nil); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if _, err := a.Authenticate(context.Background(), http.Header{}, nil); err == nil {
		t.Error("no header accepted")
	}
}

func TestLinkedIdentity(t *testing.T) {
	a, _, m, _ := setup(t)
	exp := time.Now().Add(time.Hour).Unix()
	tok := m.token(t, map[string]any{"iss": ms, "sub": "pairwise", "oid": "oid-alice", "aud": "api://workbench", "exp": exp})
	s, err := a.Authenticate(context.Background(), header(tok), nil)
	if err != nil {
		t.Fatal(err)
	}
	caller := FromPrincipal(s.Principal())
	if caller.ID != "alice-principal" || caller.Surface != "teams" || caller.Groups[0] != "requesters" {
		t.Fatalf("linked caller %+v", caller)
	}
	unlinked := m.token(t, map[string]any{"iss": ms, "oid": "oid-bob", "sub": "x", "aud": "api://workbench", "exp": exp})
	if _, err := a.Authenticate(context.Background(), header(unlinked), nil); !errors.Is(err, ErrNotLinked) {
		t.Fatalf("unlinked identity must say so, got %v", err)
	}
	iss, sub, _, err := a.VerifyLinkToken(context.Background(), unlinked)
	if err != nil || iss != ms || sub != "oid-bob" {
		t.Fatalf("link token: %v %s %s", err, iss, sub)
	}
}

func TestCompanyTokenCannotBeLinked(t *testing.T) {
	a, c, _, _ := setup(t)
	tok := c.token(t, map[string]any{"iss": company, "sub": "u", "aud": "workbench", "exp": time.Now().Add(time.Hour).Unix()})
	if _, _, _, err := a.VerifyLinkToken(context.Background(), tok); err == nil {
		t.Fatal("only linked issuers may be linked")
	}
}

func TestInternalTokens(t *testing.T) {
	a, _, _, _ := setup(t)
	tok := a.Mint("alice-principal", "bob-principal", "deliver-decision")
	s, err := a.Authenticate(context.Background(), header(tok), nil)
	if err != nil {
		t.Fatal(err)
	}
	c := FromPrincipal(s.Principal())
	if c.ID != "alice-principal" || !c.Delegated || s.Principal().Claims[ClaimActor] != "bob-principal" {
		t.Fatalf("internal %+v", c)
	}
	tampered := tok[:len(tok)-2] + "xx"
	if _, err := a.Authenticate(context.Background(), header(tampered), nil); err == nil {
		t.Fatal("tampered token accepted")
	}
	a.now = func() time.Time { return time.Now().Add(time.Hour) }
	if _, err := a.Authenticate(context.Background(), header(tok), nil); err == nil {
		t.Fatal("expired internal token accepted")
	}
}

func TestUpstreamDoesNotForwardToken(t *testing.T) {
	a, c, _, _ := setup(t)
	tok := c.token(t, map[string]any{"iss": company, "sub": "u", "aud": "workbench", "exp": time.Now().Add(time.Hour).Unix()})
	s, _ := a.Authenticate(context.Background(), header(tok), nil)
	r, _ := http.NewRequest(http.MethodGet, "http://tool", nil)
	r.Header.Set("Authorization", "Bearer leftover")
	if err := a.UpstreamAuth(r, s, ""); err != nil {
		t.Fatal(err)
	}
	if r.Header.Get("Authorization") != "" || r.Header.Get("X-User-Id") == "" {
		t.Fatalf("upstream headers %v", r.Header)
	}
}
