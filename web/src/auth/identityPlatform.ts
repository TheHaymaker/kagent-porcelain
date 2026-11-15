/**
 * Sign-in with Google Identity Platform (ADR 0005): the Firebase Auth SDK
 * signs the person in with Google in a popup, and the resulting ID token is
 * the bearer token every API call carries. Identity Platform keeps the OAuth
 * client secret; the browser holds only the public web API key.
 *
 * The SDK is loaded on demand, so builds that use generic OIDC never fetch it.
 */
import type { Auth, User } from "firebase/auth";
import type { WorkbenchConfig } from "@/config";

let auth: Promise<Auth> | undefined;

function getAuthFor(cfg: WorkbenchConfig): Promise<Auth> {
  auth ??= (async () => {
    const ip = cfg.identityPlatform;
    if (!ip) throw new Error("identityPlatform is not configured.");
    const [{ initializeApp }, fb] = await Promise.all([import("firebase/app"), import("firebase/auth")]);
    const app = initializeApp({ apiKey: ip.apiKey, authDomain: ip.authDomain, projectId: ip.projectId });
    const a = fb.getAuth(app);
    // A tab's sign-in ends with the tab, like the OIDC path's sessionStorage.
    await fb.setPersistence(a, fb.browserSessionPersistence);
    if (ip.tenantId) a.tenantId = ip.tenantId;
    return a;
  })();
  return auth;
}

/**
 * Watches the signed-in user. onToken receives the current ID token, or
 * undefined when signed out; the SDK refreshes the token before it expires
 * and calls again. Returns an unsubscribe function.
 */
export function watchIdentityPlatform(cfg: WorkbenchConfig, onToken: (token: string | undefined) => void): () => void {
  let stop: (() => void) | undefined;
  let stopped = false;
  void getAuthFor(cfg).then(async (a) => {
    const { onIdTokenChanged } = await import("firebase/auth");
    if (stopped) return;
    stop = onIdTokenChanged(a, (user: User | null) => {
      if (!user) return onToken(undefined);
      user.getIdToken().then(onToken, () => onToken(undefined));
    });
  }, () => onToken(undefined));
  return () => {
    stopped = true;
    stop?.();
  };
}

export async function signInWithIdentityPlatform(cfg: WorkbenchConfig): Promise<void> {
  const a = await getAuthFor(cfg);
  const { GoogleAuthProvider, signInWithPopup } = await import("firebase/auth");
  const provider = new GoogleAuthProvider();
  provider.setCustomParameters({ prompt: "select_account" });
  await signInWithPopup(a, provider);
}

export async function signOutOfIdentityPlatform(cfg: WorkbenchConfig): Promise<void> {
  const a = await getAuthFor(cfg);
  const { signOut } = await import("firebase/auth");
  await signOut(a);
}
