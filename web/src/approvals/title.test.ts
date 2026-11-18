import { describe, expect, it } from "vitest";
import { create } from "@bufbuild/protobuf";
import { ApprovalSchema } from "@/gen/workbench/v1/approvals_pb";
import { approvalTitle } from "./logic";

describe("approvalTitle", () => {
  it("names known tools in plain words, with their target", () => {
    const a = create(ApprovalSchema, {
      calls: [
        { name: "cloudrun_deploy_preview", args: { service: "cat-todo" } },
      ],
    });
    expect(approvalTitle(a)).toBe("Deploy the test version: cat-todo?");
  });
  it("falls back to a readable tool name", () => {
    const a = create(ApprovalSchema, {
      calls: [{ name: "gitlab_create_branch" }],
    });
    expect(approvalTitle(a)).toBe("Create branch?");
  });
  it("prefers a real hint", () => {
    const a = create(ApprovalSchema, {
      hint: "Delete the old report table",
      calls: [{ name: "bigquery_delete_table" }],
    });
    expect(approvalTitle(a)).toBe("Delete the old report table");
  });
});
