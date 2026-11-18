import { useEffect, useRef, useState } from "react";
import { useNavigate } from "react-router";
import { Bell, CheckCheck } from "lucide-react";
import { api } from "@/api/clients";
import { useRpc } from "@/api/useRpc";
import type { Notification } from "@/gen/workbench/v1/notifications_pb";
import { relativeTime } from "@/lib/format";
import { Button } from "@/ui/Button";
import { EmptyState } from "@/ui/States";

export function NotificationsBell() {
  const [open, setOpen] = useState(false);
  const navigate = useNavigate();
  const ref = useRef<HTMLDivElement>(null);
  const rpc = useRpc(async (signal) => api.notifications.listNotifications({ page: { pageSize: 20 } }, { signal }), [], { pollMs: 20000 });
  const unread = rpc.data?.unreadCount ?? 0;

  useEffect(() => {
    if (!open) return;
    const onDown = (e: MouseEvent) => {
      if (ref.current && !ref.current.contains(e.target as Node)) setOpen(false);
    };
    const onKey = (e: KeyboardEvent) => e.key === "Escape" && setOpen(false);
    document.addEventListener("mousedown", onDown);
    document.addEventListener("keydown", onKey);
    return () => {
      document.removeEventListener("mousedown", onDown);
      document.removeEventListener("keydown", onKey);
    };
  }, [open]);

  const openItem = async (n: Notification) => {
    if (!n.read) {
      await api.notifications.markRead({ ids: [n.id] }).catch(() => undefined);
      rpc.reload();
    }
    setOpen(false);
    if (n.link.startsWith("/")) navigate(n.link);
  };

  return (
    <div className="bell" ref={ref}>
      <button
        type="button"
        className="icon-btn"
        aria-label={unread ? `Notifications, ${unread} unread` : "Notifications"}
        aria-expanded={open}
        aria-haspopup="true"
        onClick={() => setOpen((o) => !o)}
      >
        <Bell size={20} aria-hidden />
        {unread > 0 && <span className="bell-dot">{unread > 9 ? "9+" : unread}</span>}
      </button>
      {open && (
        <div className="popover" role="region" aria-label="Notifications">
          <div className="popover-head">
            <strong>Notifications</strong>
            {unread > 0 && (
              <Button
                size="sm"
                variant="ghost"
                icon={<CheckCheck size={16} />}
                onClick={async () => {
                  await api.notifications.markRead({ all: true });
                  rpc.reload();
                }}
              >
                Mark all read
              </Button>
            )}
          </div>
          {rpc.data && rpc.data.notifications.length === 0 && <EmptyState title="You're all caught up" />}
          <ul className="notif-list">
            {rpc.data?.notifications.map((n) => (
              <li key={n.id}>
                <button type="button" className={`notif ${n.read ? "" : "is-unread"}`} onClick={() => void openItem(n)}>
                  {!n.read && <span className="notif-dot" aria-label="Unread" />}
                  <span className="notif-title">{n.title}</span>
                  <span className="notif-body">{n.body}</span>
                  <span className="notif-time">{relativeTime(n.createdAt)}</span>
                </button>
              </li>
            ))}
          </ul>
        </div>
      )}
    </div>
  );
}
