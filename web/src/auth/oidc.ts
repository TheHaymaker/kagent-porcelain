/**
 * Sign-in with the company account: OIDC authorization code + PKCE through
 * oidc-client-ts. The access token is what every gRPC-Web call carries.
 */
import { UserManager, WebStorageStateStore, type User } from "oidc-client-ts";
import type { WorkbenchConfig } from "@/config";

let manager: UserManager | undefined;

export function userManager(config: WorkbenchConfig): UserManager {
  manager ??= new UserManager({
    authority: config.oidcIssuer,
    client_id: config.oidcClientId,
    redirect_uri: `${window.location.origin}/auth/callback`,
    post_logout_redirect_uri: `${window.location.origin}/`,
    response_type: "code",
    scope: config.oidcScope ?? "openid profile email offline_access",
    automaticSilentRenew: true,
    userStore: new WebStorageStateStore({ store: window.sessionStorage }),
  });
  return manager;
}

/** The bearer token for API calls: the access token, or the ID token if the issuer gives no access token. */
export function bearerOf(user: User | null | undefined): string | undefined {
  if (!user || user.expired) return undefined;
  return user.access_token || user.id_token;
}
