# ADR 0002: How approvals are enforced

Status: accepted (Nov 2026).

## Decision

1. **The pause is kagent's.** An approval wraps one kagent HITL pause
   (`tool_approval_request` on an A2A task). Workbench observes it (push
   notification, or a read whenever a client lists a session's approvals),
   classifies each call with the policy (`internal/policy`), routes it, and
   when someone decides, sends the `tool_approval_response` into the same task
   as the conversation's owner, recording the real decider.
2. **Clients never answer tool approvals themselves.** They call
   `ApprovalService.Decide`. Who may decide is computed in one function,
   `policy.Routing.MayDecide`, and returned to clients as `caller_may_decide`.
3. **The rule that matters most is enforced three times.** A requester cannot
   approve their own production deploy (or IAM change):
   - in `MayDecide`;
   - by a CHECK constraint on the `approval` table;
   - by `registry.Promote`, which refuses an approval decided by its requester.
4. **Production is reached only by Workbench.** No agent tool deploys to
   production. `workbench_request_promotion` asks; on approval Workbench
   deploys the previewed image digest with credentials that agents and
   sandboxes never hold, and records one production deployment per approval
   (unique index). If an agent template forgets to gate the tool, the tool
   opens a Workbench approval instead of deploying, so the outcome is the same.
5. **Always-ask kinds are never remembered.** Production deploy, IAM change,
   data deletion and new spend accept only "once". Refusing one needs a reason.

## Known gap: direct calls from the sandbox

kagent gives an agent's sandbox network access to the MCP servers it binds.
Code in the sandbox can therefore call a preview-level always-ask tool
(`cloudrun_set_iam_invoker`, `bigquery_delete_table`, `bigquery_create_dataset`,
`cloudrun_deploy_preview` with `allow_create`) directly over HTTP, and
kagent's pause never sees it. Production is not exposed this way (point 4),
and the tool servers confine themselves to the preview project and `wb_`
datasets. Closing the gap for preview actions needs the tool servers to
require proof of approval for always-ask tools. The planned fix is a
short-lived token minted by Workbench when an approval is allowed, bound to
the call ID and verified by the tool server. It is not built yet; see
`docs/status.md`.

## Known gap: per-tool gating under the Claude harness

`MCPToolBinding.requireApproval` exists, but under the Claude harness it gates
a whole server. The agent pack gates `cloudrun-mcp` only, and Workbench's
policy turns each paused call into the right kind. BigQuery's deletion and
spend tools are therefore not paused by kagent at all today. That is the same
direct-call gap as above, and has the same fix.
