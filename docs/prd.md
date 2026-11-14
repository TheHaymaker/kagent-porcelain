# PRD: a stakeholder workspace built for kagent

Nov 14, 2026 · Brett Haymaker · Working name: **Workbench**

> Kept here as the reference for what is built. Implementation status per
> requirement is in [status.md](status.md); decisions taken while building are
> in [adr/](adr/).

## Summary

We build our own stakeholder workspace on top of kagent and use OpenBot only as the reference for what a good one does.

Three reasons to build instead of adopt:

- OpenBot's server is not in its public repository and it requires a separately licensed service, so we could not change or fully own the front door.
- OpenBot and kagent speak different protocols. Adopting both means building and maintaining a bridge. A workspace written for kagent calls kagent directly, and the bridge disappears.
- kagent already supplies much of what a workspace needs: sessions, streaming, approvals, checkpoints, scheduled runs, sandboxes, sharing and a React UI with extension points. It also runs as a Go library that accepts our own sign-in, authorization, services and database tables.

This replaces the recommendation in the earlier evaluation to trial OpenBot. Its findings on kagent, Substrate and GCP still stand, and so do five of its build items: builder agents, GCP tools, app scaffolds, the model gateway and the audit pipeline.

**The product in one sentence:** a person signs in with their company account, asks for an app in plain language, watches an agent build it in a sandbox, approves the steps that matter, and gets a running app with a record of everything that happened.

The goal is the best interface for stakeholders, not the fastest path to one. Workbench ships on the web first, then as full chat in Microsoft Teams, then as iOS and Android apps. The server and the interface components are designed for all three from the start, so each later surface is a new client and not a rewrite.

## Users and jobs

| User | What they need to do | What success looks like |
|---|---|---|
| Requester (a stakeholder, not an engineer) | Describe an app, answer the agent's questions, approve steps, try the result, ask for changes | A working app at a URL without filing a ticket or reading code |
| Approver (a manager or the platform team) | See what an agent is about to do to shared systems and allow or refuse it | Every production change has a named person behind it |
| Platform engineer | Define builder agents, tools, scaffolds and policy; watch cost and failures | New capabilities ship as reviewed changes in Git; nothing reaches production by accident |

Assumed, to be confirmed: tens of requesters at first; apps are internal web tools and data apps on Cloud Run or GKE; code lives in GitLab.

## Architecture

Workbench is one server we own and a thin client for each surface. The server embeds kagent instead of calling it over a bridge: kagent's controller with our parts compiled in — one process, one API, one database, one audit stream.

- **Clients stay thin.** Approvals, the inbox, notifications and the app registry are server APIs. A client draws them; it never decides anything.
- **One component catalogue.** Agents describe results as declarative components (A2UI). The web app draws them with our design system, the Teams bot maps them to Adaptive Cards, and the mobile apps draw them natively.
- **Approvals stay native.** The approval a requester sees is kagent's own pause on the task.
- **We track kagent closely.** The library surface and the API are alpha-versioned. We pin a version and upgrade deliberately.

## Requirements

| # | Requirement | Taken from OpenBot's | Priority | kagent already has | We build |
|---|---|---|---|---|---|
| 1 | Ask in plain language and watch the work stream in | Conversations | Must | Streaming tasks, durable history, sessions | Requester chat screen |
| 2 | Sign in with the company account on every surface | Company sign-in | Must | OIDC through a proxy, cookie-based | Token-based sign-in for web, mobile and a Teams bot; Microsoft ↔ company identity link |
| 3 | Roles decide who can use which agent and see which conversation | Roles and permissions | Must | An authorizer interface | An authorizer backed by our groups |
| 4 | Approve or refuse a pending action on a card, with a reason | Approval cards | Must | Tool-approval pause and resume | The approval card; remembered answers (once, this conversation, always) |
| 5 | Some actions always ask: production deploy, IAM change, data deletion, new spend | Always-ask action kinds | Must | Per-tool approval flags | The list, and the rule that the requester cannot approve their own production deploy |
| 6 | Route an approval to someone other than the requester, on whichever surface they use | Team approval rules | Must | Read-only session shares | An approval service: inbox, routing, delegated decision |
| 7 | A readable record of what was allowed, refused and done | Audit trail | Must | Durable task events; OpenTelemetry | Audit screen; export to BigQuery |
| 8 | See what the agent ran, read and saved | Activity list | Must | Tool calls and results in the stream | Activity panel |
| 9 | Each conversation works in its own isolated computer | A computer per person | Must | A Substrate sandbox per session | Nothing |
| 10 | Credentials never appear in the conversation or the sandbox | Secrets out of the transcript | Must | Gateway credential injection | Nothing; platform-managed at first |
| 11 | Show results as native interactive components on every surface | Components | Must | MCP App rendering (HTML, web only) | One A2UI catalogue: approval card, progress, deploy preview, app card, cost summary; a renderer per surface |
| 12 | Reusable skills | Skills | Should | Skills and plugins on agent templates | Authoring in Git first |
| 13 | Run a request on a schedule | Routines | Should | Scheduled runs | Requester-facing screen |
| 14 | Share a conversation | Sharing | Should | Session shares | Share button and viewer |
| 15 | Per-person and per-team spend limits | Token limits | Should | Usage in telemetry, uneven | Metering, limits, a cost view |
| 16 | Tell people when an agent needs them or finishes | Notifications | Should | Push notifications on tasks | Email, Teams and mobile push |
| 17 | Connect tools through MCP under policy | Governed MCP | Should | Remote MCP servers, per-agent egress | A reviewed catalogue of tool servers |
| 18 | Add agents built on other frameworks | Bring your own agent | Later | ADK, Claude Code, Codex, custom images | Nothing until asked |
| 19 | A catalogue of every app built: owner, URL, status, cost | New | Must | Nothing | App registry and screen |
| 20 | Preview first, then promote to production with approval | New | Must | Nothing | Environments and a promote action |
| 21 | Go back to an earlier point and try another approach | New | Should | Checkpoints and forks | A "try again from here" control |
| 22 | Full chat in Microsoft Teams | Slack and Teams | Later, phase 5 | Nothing | A Teams bot; catalogue → Adaptive Cards |
| 23 | iOS and Android apps | Mobile apps | Later, phase 6 | Push notifications | Native apps on the shared catalogue |

## Out of scope

Agent-driven browser; brokered SaaS apps; per-person credential vault (for now); automatic skill learning; personal memory (later); multi-agent group chats; desktop app; voice; SCIM and runtime identity providers (later). Slack is out as a chat surface.

## Milestones

1. **Runtime proof.** Substrate and stock kagent on GKE; a Claude Code builder takes one scaffold to a Cloud Run preview from kagent's own UI; spike: A2UI through kagent intact.
2. **Walking skeleton.** Workbench server with token sign-in and a first authorizer; designed web app with conversation, activity panel and approval card. One friendly requester builds a real app.
3. **Governed pilot.** Approval rules and inbox, registry with preview and promote, audit export. Five to ten requesters.
4. **General use on the web.** Cost limits, notifications, schedules, sharing, full catalogue, more scaffolds and tools.
5. **Teams.**
6. **Mobile.**

## Open questions

- What do stakeholders mean by "apps", and what are the first three?
- Who approves a production deploy?
- Is the directory Microsoft Entra, Google Workspace, or both?
- Mobile framework: Flutter, React Native, or native?
- Extend kagent's UI, or a separate requester app? (assumed: separate)
- Does the Workbench server replace kagent's controller? (assumed: replace — see ADR 0001)
- Who owns Workbench after launch, how many engineers, is there a designer?
- Is "Workbench" the name?
