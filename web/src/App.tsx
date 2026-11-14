import { BrowserRouter, Navigate, Route, Routes, useLocation } from "react-router";
import { AuthProvider, useAuth } from "./auth/AuthContext";
import { AuthCallback, SignIn } from "./auth/SignIn";
import { AppShell } from "./layout/AppShell";
import { ToastProvider } from "./ui/Toast";
import { Loading } from "./ui/States";
import { HomePage } from "./pages/HomePage";
import { ConversationPage } from "./pages/conversation/ConversationPage";
import { ApprovalsPage } from "./pages/ApprovalsPage";
import { AppsPage } from "./pages/AppsPage";
import { AppDetailPage } from "./pages/AppDetailPage";
import { SchedulesPage } from "./pages/SchedulesPage";
import { AuditPage } from "./pages/AuditPage";
import { UsagePage } from "./pages/UsagePage";
import { SettingsPage } from "./pages/SettingsPage";
import { NotFoundPage } from "./pages/NotFoundPage";

function Protected() {
  const { status } = useAuth();
  const location = useLocation();
  if (status === "loading") return <Loading label="Signing you in" />;
  if (status === "signed-out") return <SignIn returnTo={location.pathname + location.search} />;
  return (
    <AppShell>
      <Routes>
        <Route path="/" element={<HomePage />} />
        <Route path="/c/:sessionId" element={<ConversationPage />} />
        <Route path="/approvals" element={<ApprovalsPage />} />
        <Route path="/approvals/:approvalId" element={<ApprovalsPage />} />
        <Route path="/apps" element={<AppsPage />} />
        <Route path="/apps/:appId" element={<AppDetailPage />} />
        <Route path="/schedules" element={<SchedulesPage />} />
        <Route path="/audit" element={<AuditPage />} />
        <Route path="/usage" element={<UsagePage />} />
        <Route path="/settings" element={<SettingsPage />} />
        <Route path="/home" element={<Navigate to="/" replace />} />
        <Route path="*" element={<NotFoundPage />} />
      </Routes>
    </AppShell>
  );
}

export function App() {
  return (
    <BrowserRouter>
      <ToastProvider>
        <AuthProvider>
          <Routes>
            <Route path="/auth/callback" element={<AuthCallback />} />
            <Route path="/*" element={<Protected />} />
          </Routes>
        </AuthProvider>
      </ToastProvider>
    </BrowserRouter>
  );
}
