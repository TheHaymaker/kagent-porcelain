import { describe, expect, it } from "vitest";
import { expiryOf } from "./googleSignIn";

const jwt = (payload: object) =>
  `h.${btoa(JSON.stringify(payload)).replace(/=+$/, "")}.s`;

describe("expiryOf", () => {
  it("reads exp from a JWT", () => {
    expect(expiryOf(jwt({ exp: 1700000000 }))).toBe(1700000000);
  });
  it("is 0 for anything unreadable", () => {
    expect(expiryOf("garbage")).toBe(0);
    expect(expiryOf(jwt({ sub: "x" }))).toBe(0);
  });
});
