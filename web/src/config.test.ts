import { describe, expect, it } from "vitest";
import { normaliseConfig } from "./config";

describe("normaliseConfig", () => {
  it("defaults to generic OIDC", () => {
    expect(normaliseConfig({}).authProvider).toBe("oidc");
    expect(normaliseConfig({ authProvider: "something-else" as never }).authProvider).toBe("oidc");
  });

  it("keeps Identity Platform settings", () => {
    const cfg = normaliseConfig({
      authProvider: "identity-platform",
      identityPlatform: { apiKey: "browser-key", authDomain: "p.firebaseapp.com", projectId: "p" },
      apiBaseUrl: "/api/",
    });
    expect(cfg.authProvider).toBe("identity-platform");
    expect(cfg.identityPlatform?.projectId).toBe("p");
    expect(cfg.apiBaseUrl).toBe("/api");
  });
});
