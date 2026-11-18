import { afterEach, describe, expect, it, vi } from "vitest";
import { act, screen, waitFor, fireEvent } from "@testing-library/react";
import { ApprovalService } from "@/gen/workbench/v1/approvals_pb";
import { AppService, AppStatus } from "@/gen/workbench/v1/apps_pb";
import { ComponentService } from "@/gen/workbench/v1/components_pb";
import { setApiTransport } from "@/api/transport";
import { approval } from "@/test/fixtures";
import { renderWithRouter, useServices } from "@/test/render";
import catalog from "../../../catalogue/catalog.json";
import { A2uiSurface, CATALOG_COMPONENTS, resolveDynamic, resolveString } from "./Renderer";
import { SurfaceStore } from "./store";

afterEach(() => setApiTransport(undefined));

function surface(components: object[], dataModel?: unknown) {
  const store = new SurfaceStore();
  store.apply([
    { version: "v0.9.1", createSurface: { surfaceId: "s", catalogId: catalog.catalogId } },
    { version: "v0.9.1", updateComponents: { surfaceId: "s", components } },
    ...(dataModel !== undefined ? [{ version: "v0.9.1", updateDataModel: { surfaceId: "s", path: "/", value: dataModel } }] : []),
  ]);
  return store;
}

describe("A2UI renderer", () => {
  it("covers exactly the Workbench catalogue", () => {
    expect([...CATALOG_COMPONENTS].sort()).toEqual(Object.keys(catalog.components).sort());
  });

  it("renders layout, text variants and dividers from the root", () => {
    const store = surface([
      { id: "root", component: "Column", children: ["h", "card"] },
      { id: "h", component: "Text", text: "Hello", variant: "h2" },
      { id: "card", component: "Card", child: "row" },
      { id: "row", component: "Row", children: ["t", "d"] },
      { id: "t", component: "Text", text: "**bold** and `code`" },
      { id: "d", component: "Divider" },
    ]);
    const { container } = renderWithRouter(<A2uiSurface store={store} surfaceId="s" />);
    expect(screen.getByRole("heading", { name: "Hello" })).toBeInTheDocument();
    expect(container.querySelector("strong")?.textContent).toBe("bold");
    expect(container.querySelector("hr")).not.toBeNull();
  });

  it("binds data-model paths and re-renders on updateDataModel", () => {
    const store = surface(
      [
        { id: "root", component: "Column", children: ["t", "p"] },
        { id: "t", component: "Text", text: { path: "/user/name" }, variant: "h3" },
        { id: "p", component: "Progress", title: "Build", steps: [{ label: "Tests", state: "done" }], percent: { path: "/build/percent" } },
      ],
      { user: { name: "Ava" }, build: { percent: 40 } },
    );
    renderWithRouter(<A2uiSurface store={store} surfaceId="s" />);
    expect(screen.getByText("Ava")).toBeInTheDocument();
    expect(screen.getByRole("progressbar")).toHaveAttribute("aria-valuenow", "40");
    act(() => {
      store.apply([{ updateDataModel: { surfaceId: "s", path: "/build/percent", value: 90 } }]);
    });
    expect(screen.getByRole("progressbar")).toHaveAttribute("aria-valuenow", "90");
  });

  it("resolves bindings only for exact {path} objects", () => {
    expect(resolveDynamic({ path: "/a" }, { a: 1 })).toBe(1);
    expect(resolveDynamic({ path: "/a", extra: 1 }, { a: 1 })).toEqual({ path: "/a", extra: 1 });
    expect(resolveString({ path: "/n" }, { n: 3 })).toBe("3");
    expect(resolveString({ path: "/obj" }, { obj: { x: 1 } })).toBe("");
  });

  it("renders unknown components as a safe fallback", () => {
    const store = surface([{ id: "root", component: "Iframe", src: "https://evil.example" }]);
    const { container } = renderWithRouter(<A2uiSurface store={store} surfaceId="s" />);
    expect(screen.getByRole("note")).toHaveTextContent("can't be shown here");
    expect(container.querySelector("iframe")).toBeNull();
  });

  it("never injects HTML and only links https", () => {
    const store = surface([
      { id: "root", component: "Column", children: ["t", "l1", "l2", "md"] },
      { id: "t", component: "Text", text: "<img src=x onerror=alert(1)><script>alert(1)</script>", variant: "h1" },
      { id: "l1", component: "Link", label: "Bad", url: "javascript:alert(1)" },
      { id: "l2", component: "Link", label: "Good", url: "https://example.com/x" },
      { id: "md", component: "Text", text: "[click](javascript:alert(1)) [ok](https://ok.example)" },
    ]);
    const { container } = renderWithRouter(<A2uiSurface store={store} surfaceId="s" />);
    expect(container.querySelector("img")).toBeNull();
    expect(container.querySelector("script")).toBeNull();
    expect(screen.getByText(/<img src=x/)).toBeInTheDocument();
    const hrefs = [...container.querySelectorAll("a")].map((a) => a.getAttribute("href"));
    expect(hrefs).toEqual(["https://example.com/x", "https://ok.example/"]);
    expect(screen.getByText("Bad").tagName).toBe("SPAN");
  });

  it("guards against cycles", () => {
    const store = surface([
      { id: "root", component: "Column", children: ["a"] },
      { id: "a", component: "Card", child: "root" },
    ]);
    renderWithRouter(<A2uiSurface store={store} surfaceId="s" />);
    expect(screen.getByRole("note")).toHaveTextContent("nested too deeply");
  });

  it("renders nothing for a deleted surface", () => {
    const store = surface([{ id: "root", component: "Text", text: "x" }]);
    store.apply([{ deleteSurface: { surfaceId: "s" } }]);
    const { container } = renderWithRouter(<A2uiSurface store={store} surfaceId="s" />);
    expect(container.textContent).toBe("");
  });

  it("sends Button actions to ComponentService.SendAction with resolved context", async () => {
    const sendAction = vi.fn(async () => ({}));
    useServices((r) => r.service(ComponentService, { sendAction }));
    const store = surface(
      [{ id: "root", component: "Button", label: "Retry", variant: "primary", action: { event: { name: "retry", context: { step: { path: "/step" }, fixed: 1 } } } }],
      { step: "tests" },
    );
    renderWithRouter(<A2uiSurface store={store} surfaceId="s" />);
    fireEvent.click(screen.getByRole("button", { name: "Retry" }));
    await waitFor(() => expect(sendAction).toHaveBeenCalled());
    const request = (sendAction.mock.calls[0] as unknown as [{ surfaceId: string; name: string; sourceComponentId: string; context: unknown }])[0];
    expect(request).toMatchObject({ surfaceId: "s", name: "retry", sourceComponentId: "root", context: { step: "tests", fixed: 1 } });
  });

  it("draws an ApprovalCard from the ApprovalService, not from the agent's props", async () => {
    useServices((r) =>
      r.service(ApprovalService, {
        getApproval: async () => ({ approval: approval({ id: "apr_x", callerMayDecide: false, approverGroups: ["platform-approvers"] }) }),
      }),
    );
    const store = surface([{ id: "root", component: "ApprovalCard", approvalId: "apr_x", title: "Ship it", approve: true }]);
    renderWithRouter(<A2uiSurface store={store} surfaceId="s" />);
    expect(await screen.findByText("Ship it")).toBeInTheDocument();
    expect(screen.getByText(/Waiting on Platform approvers/)).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Approve" })).toBeNull();
  });

  it("refreshes an AppCard from AppService.GetApp", async () => {
    useServices((r) =>
      r.service(AppService, {
        getApp: async () => ({ app: { id: "app_1", name: "server-name", status: AppStatus.LIVE, costCentsMtd: 1234n } }),
      }),
    );
    const store = surface([{ id: "root", component: "AppCard", appId: "app_1", name: "agent-name" }]);
    renderWithRouter(<A2uiSurface store={store} surfaceId="s" />);
    expect(await screen.findByText("server-name")).toBeInTheDocument();
    expect(screen.getByText("Live")).toBeInTheDocument();
    expect(screen.getByText("$12.34")).toBeInTheDocument();
  });

  it("formats CostSummary and DeployPreview", () => {
    const store = surface([
      { id: "root", component: "Column", children: ["c", "d"] },
      { id: "c", component: "CostSummary", title: "This build", totalCents: 412, limitCents: 5000, lines: [{ label: "Model", cents: 388 }] },
      { id: "d", component: "DeployPreview", appName: "x", environment: "preview", status: "ready", url: "https://x.example" },
    ]);
    renderWithRouter(<A2uiSurface store={store} surfaceId="s" />);
    expect(screen.getAllByText("$4.12").length).toBeGreaterThan(0);
    expect(screen.getByText("$3.88")).toBeInTheDocument();
    expect(screen.getByRole("link", { name: /Open app/ })).toHaveAttribute("href", "https://x.example/");
  });
});
