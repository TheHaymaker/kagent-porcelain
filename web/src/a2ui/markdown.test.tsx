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

  it("renders the planner's plan: headings with lists right under them", () => {
    const plan = [
      "## Plan: team-todo-tracker",
      "",
      "**What it is:** A task manager.",
      "**Template:** python-fastapi-cloudrun",
      "",
      "### Phase 1: Personal task list",
      "- Builds: A home screen",
      "- Checks:",
      "  - Tasks survive a refresh",
      "  - A task needs a name",
      "### Later",
      "1. Labels",
      "2. Sorting",
    ].join("\n");
    const { container } = render(<Markdown text={plan} />);
    expect(container.querySelector("h4")?.textContent).toBe("Plan: team-todo-tracker");
    const h5 = [...container.querySelectorAll("h5")].map((h) => h.textContent);
    expect(h5).toEqual(["Phase 1: Personal task list", "Later"]);
    expect(container.querySelectorAll("ul > li").length).toBe(4);
    expect(container.querySelectorAll("li > ul > li").length).toBe(2);
    expect(container.querySelectorAll("ol > li").length).toBe(2);
    expect(container.querySelectorAll("strong").length).toBe(2);
    expect(container.textContent).not.toContain("#");
  });

  it("keeps a wrapped item's text with its item", () => {
    const { container } = render(<Markdown text={"- one\n  still one\n- two"} />);
    const items = container.querySelectorAll("li");
    expect(items.length).toBe(2);
    expect(items[0]?.textContent).toBe("onestill one");
  });

  it("renders code, quotes, rules and tables as text", () => {
    const { container } = render(
      <Markdown
        text={"```\n<b>x</b>\n```\n\n> note\n\n---\n\n| a | b |\n|---|---|\n| 1 | **2** |"}
      />,
    );
    expect(container.querySelector("pre code")?.textContent).toBe("<b>x</b>");
    expect(container.querySelector("b")).toBeNull();
    expect(container.querySelector("blockquote")?.textContent).toBe("note");
    expect(container.querySelector("hr")).not.toBeNull();
    expect(container.querySelectorAll("td").length).toBe(2);
    expect(container.querySelector("td strong")?.textContent).toBe("2");
  });
});
