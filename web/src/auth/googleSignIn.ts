/**
 * Sign-in with Google Identity Services (the "Sign in with Google" button):
 * Google hands the page an ID token (issuer https://accounts.google.com,
 * audience = the OAuth client ID) and that token is the bearer token every
 * API call carries. No popup or redirect handler of ours is involved, which
 * makes it reliable on mobile browsers. Tokens last an hour; Google's
 * automatic sign-in renews them silently when it can.
 */
import type { WorkbenchConfig } from "@/config";

interface CredentialResponse {
  credential: string;
}

interface GoogleId {
  initialize(options: {
    client_id: string;
    callback: (r: CredentialResponse) => void;
    auto_select?: boolean;
    use_fedcm_for_prompt?: boolean;
    itp_support?: boolean;
  }): void;
  renderButton(el: HTMLElement, options: Record<string, unknown>): void;
  prompt(): void;
  disableAutoSelect(): void;
}

declare global {
  interface Window {
    google?: { accounts: { id: GoogleId } };
  }
}

const STORE = "workbench.googleIdToken";
let ready: Promise<GoogleId> | undefined;
let listener: ((token: string | undefined) => void) | undefined;
let renewTimer: number | undefined;

/** Seconds since the epoch at which a JWT expires, or 0 if unreadable. */
export function expiryOf(token: string): number {
  try {
    const part = token.split(".")[1] ?? "";
    const json = atob(part.replace(/-/g, "+").replace(/_/g, "/"));
    const exp = (JSON.parse(json) as { exp?: number }).exp;
    return typeof exp === "number" ? exp : 0;
  } catch {
    return 0;
  }
}

function valid(token: string | null | undefined): token is string {
  return !!token && expiryOf(token) - 60 > Date.now() / 1000;
}

function accept(token: string | undefined) {
  if (renewTimer) window.clearTimeout(renewTimer);
  if (valid(token)) {
    window.sessionStorage.setItem(STORE, token);
    // Ask Google for a fresh token a little before this one expires.
    const ms = (expiryOf(token) - 120) * 1000 - Date.now();
    renewTimer = window.setTimeout(
      () => {
        void load().then((id) => id.prompt());
        window.setTimeout(() => {
          const t = window.sessionStorage.getItem(STORE);
          if (!valid(t)) accept(undefined);
        }, 150 * 1000);
      },
      Math.max(ms, 0),
    );
    listener?.(token);
  } else {
    window.sessionStorage.removeItem(STORE);
    listener?.(undefined);
  }
}

let clientId = "";

function load(): Promise<GoogleId> {
  ready ??= new Promise<GoogleId>((resolve, reject) => {
    const done = () => {
      const id = window.google?.accounts.id;
      if (!id) return reject(new Error("Google sign-in did not load."));
      id.initialize({
        client_id: clientId,
        callback: (r) => accept(r.credential),
        auto_select: true,
        use_fedcm_for_prompt: true,
        itp_support: true,
      });
      resolve(id);
    };
    if (window.google?.accounts.id) return done();
    const s = document.createElement("script");
    s.src = "https://accounts.google.com/gsi/client";
    s.async = true;
    s.onload = done;
    s.onerror = () =>
      reject(
        new Error(
          "Google sign-in could not be loaded. Check your connection and reload.",
        ),
      );
    document.head.appendChild(s);
  });
  return ready;
}

/** Starts watching sign-in; onToken gets the current token or undefined. */
export function watchGoogle(
  cfg: WorkbenchConfig,
  onToken: (token: string | undefined) => void,
): () => void {
  clientId = cfg.googleClientId ?? "";
  listener = onToken;
  const stored = window.sessionStorage.getItem(STORE);
  if (valid(stored)) accept(stored);
  else onToken(undefined);
  return () => {
    listener = undefined;
    if (renewTimer) window.clearTimeout(renewTimer);
  };
}

/** Draws Google's sign-in button into el and offers One Tap. */
export async function renderGoogleButton(el: HTMLElement): Promise<void> {
  const id = await load();
  id.renderButton(el, {
    theme: "filled_blue",
    size: "large",
    shape: "pill",
    text: "signin_with",
    width: 280,
  });
  id.prompt();
}

export async function signOutOfGoogle(): Promise<void> {
  window.sessionStorage.removeItem(STORE);
  if (window.google?.accounts.id) window.google.accounts.id.disableAutoSelect();
  accept(undefined);
}
