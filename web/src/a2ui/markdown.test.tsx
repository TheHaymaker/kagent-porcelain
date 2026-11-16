import { describe, expect, it } from "vitest";
import { render } from "@testing-library/react";
import { Markdown } from "./markdown";

describe("Markdown", () => {
  it("renders a link inside bold text", () => {
    const url = "https://wb-preview-cat-todo-app-cbb4o73vwq-uc.a.run.app";
    const { container } = render(
      <Markdown text={`Open it here: **[${url}](${url})**`} />,
    );
    const a = container.querySelector("strong a");
    expect(a?.getAttribute("href")).toBe(`${url}/`);
    expect(container.textContent).not.toContain("**");
    expect(container.textContent).not.toContain("](");
  });

  it("links bare https addresses without trailing punctuation", () => {
    const { container } = render(
      <Markdown text="Try https://example.com/app. Thanks" />,
    );
    expect(container.querySelector("a")?.getAttribute("href")).toBe(
      "https://example.com/app",
    );
  });

  it("never links other schemes", () => {
    const { container } = render(
      <Markdown text="[x](javascript:alert(1)) and http://example.com" />,
    );
    expect(container.querySelector("a")).toBeNull();
  });
});
