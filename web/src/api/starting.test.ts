import { describe, expect, it } from "vitest";
import { Code, ConnectError } from "@connectrpc/connect";
import { friendlyError } from "./errors";
import { isStartingError, isStarting, markStarting, onStarting, serverIsUp } from "./starting";

const startingError = () => new ConnectError("HTTP 503", Code.Unavailable, new Headers({ "x-workbench-starting": "1" }));

describe("starting", () => {
  it("recognises the waker's answer and explains it", () => {
    expect(isStartingError(startingError())).toBe(true);
    expect(isStartingError(new ConnectError("HTTP 503", Code.Unavailable))).toBe(false);
    expect(friendlyError(startingError())).toMatch(/starting up/);
    expect(friendlyError(new ConnectError("HTTP 503", Code.Unavailable))).toMatch(/couldn't reach/);
  });

  it("tells listeners once", () => {
    const seen: boolean[] = [];
    const off = onStarting((s) => seen.push(s));
    markStarting();
    markStarting();
    off();
    expect(seen).toEqual([true]);
    expect(isStarting()).toBe(true);
  });

  it("knows when the server answers again", async () => {
    const respond = (status: number, starting: boolean) => (async () =>
      new Response(null, { status, headers: starting ? { "x-workbench-starting": "1" } : {} })) as unknown as typeof fetch;
    expect(await serverIsUp("/api", respond(503, true))).toBe(false);
    expect(await serverIsUp("/api", respond(502, false))).toBe(false);
    expect(await serverIsUp("/api", respond(404, false))).toBe(true);
    expect(await serverIsUp("/api", (async () => { throw new TypeError("offline"); }) as unknown as typeof fetch)).toBe(false);
  });
});
