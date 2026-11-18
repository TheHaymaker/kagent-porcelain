# ADR 0004: Bearer tokens and identity links

Status: accepted (Nov 2026), pending the open question on Entra versus Google
Workspace.

- Every surface sends a bearer token. Web and mobile use the company OIDC
  issuer (authorization code + PKCE). The principal ID is kagent's
  `PrincipalID(issuer, sub)`, so kagent's ownership rules apply unchanged.
- Issuers of kind `linked` (Microsoft, for the Teams bot) never sign anyone
  in on their own. A token from one resolves to a company person only through
  an identity link. The person creates that link once, from a company sign-in,
  by presenting a token from the linked issuer
  (`IdentityService.LinkIdentity`). An identity can be linked to only one
  person. Groups for a linked sign-in come from that person's last company
  sign-in.
- If the directory is Entra for everyone, the Microsoft issuer becomes the
  company issuer and linking is unnecessary. The code needs only a
  configuration change.
- The server's own loopback calls use HMAC-signed tokens with a two-minute
  lifetime that name the principal they act as and the actor they act for.
  These tokens never leave the process.
- Caller tokens are never forwarded upstream. `UpstreamAuth` sends only
  `X-User-Id`.
