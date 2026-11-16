# ADR 0005: Public exposure behind sign-in

Status: accepted (Nov 2026) for test clusters. The issuer changes when the
directory question (Entra or Google Workspace) is settled; nothing else here
does.

## Decision

- **One public surface.** Only the web service is exposed. Its nginx serves
  the app and proxies `/api/` to the Workbench server (gRPC-Web and kagent's
  A2A). The controller's other ports, `/mcp`, the hooks listener and
  Postgres stay cluster-internal.
- **Edge.** GKE's Ingress (the external Application Load Balancer) with a
  reserved static IP, a `ManagedCertificate`, and a `BackendConfig` that
  sets a one-hour backend timeout (chat turns stream) and attaches Cloud
  Armor. The Gateway API would do the same with more moving parts
  (Certificate Manager, policy attachments); nothing here depends on the
  choice. The hostname is
  `<ip-with-dashes>.sslip.io` until a real domain exists, which lets Google
  issue the certificate with no DNS setup. While only the team is testing, a
  Cloud Armor policy allows listed source IP ranges and denies the rest, so
  an auth bug is not internet-facing.
- **Identity in the application, not at the edge.** The web app signs in
  with Google Identity Platform through its JavaScript SDK (Google sign-in
  by redirect) and sends the resulting ID token as the bearer token, as
  every surface does (ADR 0004). The server trusts it as one more company
  issuer: issuer `https://securetoken.google.com/<project>`, audience
  `<project>`, keys from
  `https://www.googleapis.com/service_accounts/v1/jwk/securetoken@system.gserviceaccount.com`.
  Identity Platform holds the Google OAuth client secret, so the browser
  never needs one. The web app keeps its generic OIDC (code + PKCE) path for
  a company directory that speaks standard OIDC.
- **Roles by verified email.** Identity Platform tokens carry no groups. An
  issuer may list `emailGroups`: groups granted to a token whose email is in
  the list and whose `email_verified` claim is true. Token groups, when an
  issuer sends them, are kept; the mapped groups are added.
- **No test issuer in public.** With `publicExposure: true` the server
  refuses to start if any issuer reads its keys from a file (`jwksFile`),
  which is how the smoke test's issuer works. Anyone holding that signing key
  could otherwise mint tokens.
- **Previews are not public.** Cloud Run previews the builder deploys
  require authentication (no `allUsers` invoker), so exposing Workbench does
  not expose every preview.

## Considered

- **Identity-Aware Proxy.** Gates traffic before it reaches the app with no
  login code. Rejected as the identity mechanism: the identity arrives in a
  header only the web path sees, so Teams and mobile would need a second
  mechanism, and on a project without a Workspace organization IAP still
  needs a console-created OAuth client. It remains a good extra perimeter
  for production.
- **Plain Google OAuth from the browser.** Google's token endpoint wants a
  client secret even with PKCE, which a single-page app cannot keep.
- **Custom claims for roles** (set through the Identity Platform admin API).
  Kept as an option; an email list in reviewed configuration is simpler to
  audit while there are few users.

## Manual steps (once per project)

Enable Identity Platform, add the Google provider (this creates its OAuth
client), and add the public hostname to its authorized domains. Everything
else is `deploy/gcp/up.sh expose`.

## Amendment (16 Nov 2026): Google Identity Services, and IAP for previews

- **Sign-in uses Google Identity Services**, not the Identity Platform SDK.
  The SDK's popup flow lost its result on mobile browsers (the handler's
  storage is partitioned as a third-party site), and its redirect flow never
  completed on Edge for Android even with the handler proxied on
  Workbench's own host. Google's "Sign in with Google" button gives the page
  a Google ID token directly; the server trusts `https://accounts.google.com`
  for the web app's OAuth client ID, with roles by verified email as before.
  The Identity Platform path remains in the code for when it is wanted.
- **Previews are not public; they sit behind IAP.** Cloud Run services are
  deployed with IAP on. The requester is granted
  `roles/iap.httpsResourceAccessor` at deploy; others only through the
  approval-gated `cloudrun_share_preview`. Without a Workspace organization,
  IAP uses the web app's OAuth client (its secret is a deploy secret), whose
  redirect URIs include IAP's `…/clientIds/<id>:handleRedirect`.
