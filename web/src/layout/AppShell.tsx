import { useEffect, useState, type ReactNode } from "react";
import { NavLink, useLocation } from "react-router";
import { AppWindow, CalendarClock, CheckSquare, Gauge, History, Menu, Plus, Settings, X } from "lucide-react";
import { api } from "@/api/clients";
import { useRpc } from "@/api/useRpc";
import { useAuth } from "@/auth/AuthContext";
import { InboxView } from "@/gen/workbench/v1/approvals_pb";
import { personName } from "@/lib/format";
import { IconButton } from "@/ui/Button";
import { NotificationsBell } from "./NotificationsBell";

const NAV = [
  { to: "/approvals", label: "Approvals", icon: CheckSquare, badge: true },
  { to: "/apps", label: "Apps", icon: AppWindow },
  { to: "/schedules", label: "Schedules", icon: CalendarClock },
  { to: "/audit", label: "Activity", icon: History },
  { to: "/usage", label: "Usage", icon: Gauge },
  { to: "/settings", label: "Settings", icon: Settings },
];

export function AppShell({ children }: { children: ReactNode }) {
  const [menuOpen, setMenuOpen] = useState(false);
  const location = useLocation();
  const { me, signOut } = useAuth();
  const toDecide = useRpc(
    async (signal) => (await api.approvals.listInbox({ view: InboxView.TO_DECIDE }, { signal })).approvals.length,
    [location.pathname],
    { pollMs: 30000 },
  );

  useEffect(() => setMenuOpen(false), [location.pathname]);

  const nav = (
    <nav aria-label="Main" className="nav">
      <NavLink to="/" end className="nav-new">
        <Plus size={18} aria-hidden />
        <span>New request</span>
      </NavLink>
      <ul>
        {NAV.map((item) => (
          <li key={item.to}>
            <NavLink to={item.to} className="nav-link">
              <item.icon size={18} aria-hidden />
              <span>{item.label}</span>
              {item.badge && (toDecide.data ?? 0) > 0 && (
                <span className="nav-badge" aria-label={`${toDecide.data} waiting for you`}>
                  {toDecide.data}
                </span>
              )}
            </NavLink>
          </li>
        ))}
      </ul>
    </nav>
  );

  return (
    <div className="shell">
      <a href="#main" className="skip-link">
        Skip to content
      </a>
      <header className="topbar">
        <IconButton label="Open menu" className="menu-btn" onClick={() => setMenuOpen(true)} aria-expanded={menuOpen}>
          <Menu size={20} />
        </IconButton>
        <NavLink to="/" className="brand" aria-label="Workbench home">
          <span className="brand-mark" aria-hidden>
            W
          </span>
          <span className="brand-name">Workbench</span>
        </NavLink>
        <div className="topbar-right">
          <NotificationsBell />
          <div className="me" title={me?.person?.email}>
            <span className="avatar" aria-hidden>
              {initials(personName(me?.person))}
            </span>
            <span className="me-name">{me?.person ? personName(me.person) : ""}</span>
          </div>
        </div>
      </header>
      <aside className="sidebar">{nav}</aside>
      {menuOpen && (
        <div className="mobile-nav" role="dialog" aria-modal="true" aria-label="Menu">
          <div className="mobile-nav-backdrop" onClick={() => setMenuOpen(false)} />
          <div className="mobile-nav-panel">
            <div className="mobile-nav-head">
              <span className="brand-name">Workbench</span>
              <IconButton label="Close menu" onClick={() => setMenuOpen(false)} autoFocus>
                <X size={20} />
              </IconButton>
            </div>
            {nav}
            <button type="button" className="nav-link signout" onClick={() => void signOut()}>
              Sign out
            </button>
          </div>
        </div>
      )}
      <main id="main" className="main" tabIndex={-1}>
        {children}
      </main>
    </div>
  );
}

function initials(name: string): string {
  return name
    .split(/\s+/)
    .map((w) => w[0] ?? "")
    .join("")
    .slice(0, 2)
    .toUpperCase();
}
