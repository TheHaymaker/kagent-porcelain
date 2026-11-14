# ADR 0003: One A2UI catalogue, carried three ways

Status: accepted (Nov 2026).

Agents describe results as A2UI v0.9.1 messages against the Workbench
catalogue (`catalogue/catalog.json`): Text, Column, Row, Card, Divider, Link,
Button, ApprovalCard, Progress, DeployPreview, AppCard, CostSummary. v0.9.1 is
current; 1.0 was a release candidate when checked.

**Carriers.** Agents call the `workbench_show` MCP tool. The server validates
the messages against the catalogue (unknown components, properties, non-https
links, foreign catalogues and oversized surfaces are refused), stores the
surface, and returns it as the tool's structured result `{a2ui: {surfaceId,
messages}}`. A client therefore receives it:

1. in the A2A stream, as that tool result, which is the PRD's fallback carrier
   and works today;
2. as an A2A DataPart with `metadata.mimeType = application/a2ui+json`, if
   kagent passes an agent's DataParts through. This is the phase 1 spike, not
   yet run;
3. from `ComponentService.ListSurfaces`, for surfaces that poll (Teams,
   mobile) or after a reload.

Clients de-duplicate by `surfaceId`.

**Trust.** Agents cannot draw authority. An ApprovalCard carries only an
`approvalId`; clients fetch the approval and its `caller_may_decide` from the
server. The server also refuses an ApprovalCard for an approval outside the
conversation. AppCard refreshes from the registry.

**Renderers.** The web app has its own renderer for this catalogue. Teams
(Adaptive Cards) and mobile will each map the same twelve components.
