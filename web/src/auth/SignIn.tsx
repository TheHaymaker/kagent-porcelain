import { useEffect, useRef, useState } from "react";
import { useNavigate } from "react-router";
import { LogIn } from "lucide-react";
import { Button } from "@/ui/Button";
import { ErrorBanner, Loading } from "@/ui/States";
import { useAuth } from "./AuthContext";

export function SignIn({ returnTo }: { returnTo: string }) {
  const { signIn, error: authError } = useAuth();
  const [busy, setBusy] = useState(false);
  const [localError, setError] = useState<string>();
  const error = localError ?? authError;
  return (
    <main className="signin">
      <div className="signin-card">
        <div className="brand brand-lg" aria-hidden>
          <span className="brand-mark">W</span>
        </div>
        <h1>Welcome to Workbench</h1>
        <p className="muted">
          Describe the app you need in plain words. A builder agent makes it,
          checks in when something matters, and hands you a working link.
        </p>
        {error && <ErrorBanner message={error} />}
        <Button
          variant="primary"
          size="lg"
          icon={<LogIn size={18} />}
          busy={busy}
          onClick={async () => {
            setBusy(true);
            try {
              await signIn(returnTo);
            } catch (e) {
              setError(e instanceof Error ? e.message : "Sign-in failed.");
              setBusy(false);
            }
          }}
        >
          Sign in with your company account
        </Button>
        <p className="muted small">
          You'll be sent to your company's sign-in page and brought straight
          back.
        </p>
      </div>
    </main>
  );
}

export function AuthCallback() {
  const { completeSignIn } = useAuth();
  const navigate = useNavigate();
  const [error, setError] = useState<string>();
  const done = useRef(false);
  useEffect(() => {
    if (done.current) return;
    done.current = true;
    completeSignIn()
      .then((to) => navigate(to, { replace: true }))
      .catch((e: unknown) =>
        setError(e instanceof Error ? e.message : "Sign-in failed."),
      );
  }, [completeSignIn, navigate]);
  if (error)
    return (
      <main className="signin">
        <div className="signin-card">
          <h1>Sign-in didn't finish</h1>
          <ErrorBanner message={error} />
          <Button
            variant="primary"
            onClick={() => navigate("/", { replace: true })}
          >
            Start again
          </Button>
        </div>
      </main>
    );
  return <Loading label="Finishing sign-in" />;
}
