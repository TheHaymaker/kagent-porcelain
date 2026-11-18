import type { ReactElement } from "react";
import { render } from "@testing-library/react";
import { MemoryRouter } from "react-router";
import { createRouterTransport, type ConnectRouter } from "@connectrpc/connect";
import { setApiTransport } from "@/api/transport";

export function renderWithRouter(ui: ReactElement) {
  return render(<MemoryRouter>{ui}</MemoryRouter>);
}

/** Serves the given service implementations in-process for one test. */
export function useServices(routes: (router: ConnectRouter) => void) {
  setApiTransport(createRouterTransport(routes));
}
