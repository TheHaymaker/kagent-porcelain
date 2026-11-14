---
name: emitting-components
description: How to show the requester structured UI in Workbench with the workbench_show tool - A2UI v0.9.1 messages against the Workbench catalogue, with exact examples for Progress, DeployPreview, AppCard, CostSummary and ApprovalCard, and how to update a surface. Use at every step boundary of a build and whenever a link, approval or cost should be shown.
---

# Emitting components with `workbench_show`

Workbench renders the same components on web, Teams and mobile. You describe
UI as A2UI **v0.9.1** messages; you never write HTML or Markdown tables for
build status. Only components in the Workbench catalogue
(`https://workbench.dev/catalogs/workbench/v1/catalog.json`) render:

`Text`, `Column`, `Row`, `Card`, `Divider`, `Link`, `Button` (layout) and
`Progress`, `DeployPreview`, `AppCard`, `CostSummary`, `ApprovalCard` (domain).

## The tool

```
workbench_show({ "messages": [A2UI message, ...] })   // 1-n messages, ONE surface
```

- A **surface** is one live block in the conversation. Create it once, then
  update it with the same `surfaceId`; Workbench re-renders it in place.
- Each message carries `"version": "v0.9.1"` and exactly one of
  `createSurface`, `updateComponents`, `updateDataModel`, `deleteSurface`. Every
  message in one call must name the same `surfaceId` (letters, digits, `_ - : .`,
  at most 128). `createSurface` must use the Workbench `catalogId`.
- The result echoes the accepted messages as `{"a2ui": {"surfaceId", "messages"}}`.
- `updateComponents` sends a flat list. One component has `"id": "root"`;
  containers reference children by id. Re-sending a component id replaces it.
- A string or number property may be a literal or a binding
  `{"path": "/json/pointer"}` into the surface's data model, updated with
  `updateDataModel`. Use bindings for values that change often (percent).
- Surface ids: `build-<appName>`, `deploy-<appName>-<n>`, `approval-<approvalId>`,
  `done-<appName>`.
- Workbench validates every message against the catalogue and rejects the call
  if anything is off; fix and resend rather than falling back to text. It also
  refuses an ApprovalCard whose `approvalId` is not one of this conversation's.

## Progress: the build plan (create once, update as steps change)

```json
{
  "messages": [
    {"version": "v0.9.1", "createSurface": {"surfaceId": "build-supplier-issues", "catalogId": "https://workbench.dev/catalogs/workbench/v1/catalog.json"}},
    {"version": "v0.9.1", "updateComponents": {"surfaceId": "build-supplier-issues", "components": [
      {"id": "root", "component": "Progress", "title": "Building supplier-issues",
       "steps": [
         {"label": "Understand what you need", "state": "done"},
         {"label": "Set up the app", "state": "running", "detail": "Starting from the simple web page template"},
         {"label": "Build the features", "state": "pending"},
         {"label": "Check it works", "state": "pending"},
         {"label": "Put a test version online", "state": "pending"}
       ],
       "percent": {"path": "/build/percent"}}
    ]}},
    {"version": "v0.9.1", "updateDataModel": {"surfaceId": "build-supplier-issues", "path": "/build", "value": {"percent": 20}}}
  ]
}
```

Step states: `pending`, `running`, `done`, `failed`, `skipped`. At most 30 steps;
keep it to 4-7. Later, to advance: resend the `Progress` component with new
states (same id `root`) and/or just update `/build/percent`:

```json
{"messages": [
  {"version": "v0.9.1", "updateDataModel": {"surfaceId": "build-supplier-issues", "path": "/build", "value": {"percent": 60}}}
]}
```

## DeployPreview: a test version

While deploying, then again when ready (same surface, same component id):

```json
{
  "messages": [
    {"version": "v0.9.1", "createSurface": {"surfaceId": "deploy-supplier-issues-1", "catalogId": "https://workbench.dev/catalogs/workbench/v1/catalog.json"}},
    {"version": "v0.9.1", "updateComponents": {"surfaceId": "deploy-supplier-issues-1", "components": [
      {"id": "root", "component": "DeployPreview", "appId": "app_01", "appName": "supplier-issues",
       "environment": "preview", "status": "deploying"}
    ]}}
  ]
}
```

```json
{"messages": [
  {"version": "v0.9.1", "updateComponents": {"surfaceId": "deploy-supplier-issues-1", "components": [
    {"id": "root", "component": "DeployPreview", "appId": "app_01", "appName": "supplier-issues",
     "environment": "preview", "status": "ready",
     "url": "https://supplier-issues-preview-abc123-ew.a.run.app",
     "revision": "supplier-issues-00007-xid"}
  ]}}
]}
```

`url` and `logsUrl` must be `https://`. `environment` is `preview` for
everything you deploy; `production` only appears when describing an app that
Workbench already promoted. On failure use `"status": "failed"` and, if you have
it, `logsUrl`.

## ApprovalCard: after `workbench_request_promotion`

```json
{
  "messages": [
    {"version": "v0.9.1", "createSurface": {"surfaceId": "approval-apr_01", "catalogId": "https://workbench.dev/catalogs/workbench/v1/catalog.json"}},
    {"version": "v0.9.1", "updateComponents": {"surfaceId": "approval-apr_01", "components": [
      {"id": "root", "component": "ApprovalCard", "approvalId": "apr_01",
       "title": "Make supplier-issues live",
       "summary": "Test version supplier-issues-00007-xid has passed its checks and review."}
    ]}}
  ]
}
```

The card's buttons, state and who may decide come from Workbench's
ApprovalService, not from you. Use only an `approvalId` that
`workbench_request_promotion` returned with `status: "pending"`. Never draw approve/reject `Button`s
yourself.

## AppCard + CostSummary: finishing

```json
{
  "messages": [
    {"version": "v0.9.1", "createSurface": {"surfaceId": "done-supplier-issues", "catalogId": "https://workbench.dev/catalogs/workbench/v1/catalog.json"}},
    {"version": "v0.9.1", "updateComponents": {"surfaceId": "done-supplier-issues", "components": [
      {"id": "root", "component": "Column", "children": ["heading", "app", "cost"]},
      {"id": "heading", "component": "Text", "text": "Your app is ready to try", "variant": "h2"},
      {"id": "app", "component": "AppCard", "appId": "app_01", "name": "supplier-issues",
       "description": "Log supplier issues and see the open ones."},
      {"id": "cost", "component": "CostSummary", "title": "This build", "period": "this conversation",
       "totalCents": 412, "lines": [{"label": "Model", "cents": 388}, {"label": "Test version hosting", "cents": 24}]}
    ]}}
  ]
}
```

AppCard shows owner, status and URLs from Workbench by `appId`; you only give
id, name and description. Only show a CostSummary with figures Workbench gave
you; never estimate costs yourself. If no tool returned cost figures, leave the
CostSummary out (Workbench shows usage itself). Add `limitCents` only when
Workbench reports a limit.

## Layout helpers

- `Column` / `Row` with `children` (ids), `Card` with one `child`, `Divider`.
- `Text` variants: `h1`, `h2`, `h3`, `body` (Markdown allowed), `caption`.
- `Link` (`label`, https `url`) for documentation or the repository.
- `Button` sends an action event back to you: `{"label": "Try again",
  "action": {"event": {"name": "retry_deploy", "context": {"appId": "app_01"}}}}`.
  Use sparingly; questions belong in the ask-user tool.
