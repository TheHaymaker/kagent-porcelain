import { Code, ConnectError } from "@connectrpc/connect";

/** A sentence a non-engineer can act on, for any failed call. */
export function friendlyError(error: unknown): string {
  if (error instanceof ConnectError) {
    switch (error.code) {
      case Code.Unauthenticated:
        return "Your sign-in has expired. Please sign in again.";
      case Code.PermissionDenied:
        return "You don't have permission to do that.";
      case Code.NotFound:
        return "We couldn't find that. It may have been removed.";
      case Code.FailedPrecondition:
        return error.rawMessage || "That can't be done right now.";
      case Code.ResourceExhausted:
        return "You've reached a usage limit. Check the Usage page for details.";
      case Code.Unavailable:
      case Code.Unknown:
        return "We couldn't reach Workbench. Check your connection and try again.";
      case Code.Canceled:
        return "Stopped.";
      default:
        return error.rawMessage || "Something went wrong.";
    }
  }
  if (error instanceof Error) return error.message;
  return "Something went wrong.";
}

export function isAbort(error: unknown): boolean {
  return (
    (error instanceof DOMException && error.name === "AbortError") ||
    (error instanceof ConnectError && error.code === Code.Canceled)
  );
}
