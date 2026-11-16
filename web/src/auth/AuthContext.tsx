import {
  createContext,
  useCallback,
  useContext,
  useEffect,
  useMemo,
  useState,
  type ReactNode,
} from "react";
import type { User } from "oidc-client-ts";
import { config } from "@/config";
import { registerAuthTokenSource } from "@/api/transport";
import { api } from "@/api/clients";
import type { GetMeResponse } from "@/gen/workbench/v1/identity_pb";
import { bearerOf, userManager } from "./oidc";
import {
  signInWithIdentityPlatform,
  signOutOfIdentityPlatform,
  watchIdentityPlatform,
} from "./identityPlatform";

export type AuthStatus = "loading" | "signed-out" | "signed-in";

interface AuthValue {
  status: AuthStatus;
  me: GetMeResponse | undefined;
  /** Why the last sign-in attempt failed, when the provider says. */
  error?: string;
  signIn: (returnTo?: string) => Promise<void>;
  signOut: () => Promise<void>;
  completeSignIn: () => Promise<string>;
}

const AuthContext = createContext<AuthValue | undefined>(undefined);

export function AuthProvider({ children }: { children: ReactNode }) {
  const cfg = config();
  const [user, setUser] = useState<User | null>(null);
  const [status, setStatus] = useState<AuthStatus>("loading");
  const [me, setMe] = useState<GetMeResponse>();
  const [error, setError] = useState<string>();

  useEffect(() => {
    if (cfg.mock) {
      registerAuthTokenSource(() => "mock-token");
      setStatus("signed-in");
      return;
    }
    if (cfg.authProvider === "identity-platform") {
      let token: string | undefined;
      registerAuthTokenSource(() => token);
      return watchIdentityPlatform(
        cfg,
        (t) => {
          if (t) setError(undefined);
          const changedUser = (token === undefined) !== (t === undefined);
          token = t;
          setStatus(t ? "signed-in" : "signed-out");
          if (changedUser) setUser(null);
        },
        setError,
      );
    }
    const um = userManager(cfg);
    let current: User | null = null;
    registerAuthTokenSource(() => bearerOf(current));
    const onLoaded = (u: User) => {
      current = u;
      setUser(u);
      setStatus("signed-in");
    };
    const onUnloaded = () => {
      current = null;
      setUser(null);
      setStatus("signed-out");
    };
    um.events.addUserLoaded(onLoaded);
    um.events.addUserUnloaded(onUnloaded);
    um.events.addSilentRenewError(onUnloaded);
    void um.getUser().then((u) => {
      if (u && !u.expired) onLoaded(u);
      else setStatus("signed-out");
    });
    return () => {
      um.events.removeUserLoaded(onLoaded);
      um.events.removeUserUnloaded(onUnloaded);
      um.events.removeSilentRenewError(onUnloaded);
    };
  }, [cfg]);

  useEffect(() => {
    if (status !== "signed-in") return;
    let cancelled = false;
    api.identity
      .getMe({})
      .then((r) => !cancelled && setMe(r))
      .catch(() => undefined);
    return () => {
      cancelled = true;
    };
  }, [status, user]);

  const signIn = useCallback(
    async (returnTo?: string) => {
      if (cfg.mock) return;
      if (cfg.authProvider === "identity-platform")
        return signInWithIdentityPlatform(cfg);
      await userManager(cfg).signinRedirect({
        state: {
          returnTo:
            returnTo ?? window.location.pathname + window.location.search,
        },
      });
    },
    [cfg],
  );
  const signOut = useCallback(async () => {
    if (cfg.mock) return;
    if (cfg.authProvider === "identity-platform")
      return signOutOfIdentityPlatform(cfg);
    await userManager(cfg).signoutRedirect();
  }, [cfg]);
  const completeSignIn = useCallback(async () => {
    const u = await userManager(cfg).signinRedirectCallback();
    const state = u.state as { returnTo?: string } | undefined;
    return state?.returnTo && state.returnTo.startsWith("/")
      ? state.returnTo
      : "/";
  }, [cfg]);

  const value = useMemo(
    () => ({ status, me, error, signIn, signOut, completeSignIn }),
    [status, me, error, signIn, signOut, completeSignIn],
  );
  return <AuthContext.Provider value={value}>{children}</AuthContext.Provider>;
}

export function useAuth(): AuthValue {
  const value = useContext(AuthContext);
  if (!value) throw new Error("useAuth outside AuthProvider");
  return value;
}

/** The signed-in person's principal ID, when known. */
export function useMyId(): string | undefined {
  return useContext(AuthContext)?.me?.person?.id;
}
