import { Link } from "react-router";
import { EmptyState } from "@/ui/States";

export function NotFoundPage() {
  return (
    <div className="page">
      <EmptyState title="We couldn't find that page" action={<Link to="/">Go home</Link>}>
        The link may be old, or the thing it pointed to was removed.
      </EmptyState>
    </div>
  );
}
