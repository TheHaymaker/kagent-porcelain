import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import { loadConfig } from "./config";
import { setApiBaseUrl, setApiTransport } from "./api/transport";
import { App } from "./App";
import "./styles/tokens.css";
import "./styles/app.css";

async function boot() {
  const root = createRoot(document.getElementById("root")!);
  try {
    const cfg = await loadConfig();
    setApiBaseUrl(cfg.apiBaseUrl);
    if (cfg.mock) {
      // Fixtures are served under the real transport chain, so mock and live
      // share every code path above the wire.
      const { createMockTransport } = await import("./mock/transport");
      setApiTransport(createMockTransport());
    }
    root.render(
      <StrictMode>
        <App />
      </StrictMode>,
    );
  } catch (error) {
    root.render(
      <div className="boot-error" role="alert">
        <h1>Workbench couldn't start</h1>
        <p>{error instanceof Error ? error.message : String(error)}</p>
      </div>,
    );
  }
}

void boot();
