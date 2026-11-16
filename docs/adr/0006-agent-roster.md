# ADR 0006: The agent roster: a builder that plans, tests and reviews

Status: accepted (Nov 2026) for the Gemini builder on the test cluster.

## Context

The first end-to-end app (a cat todo app) worked, but one agent did
everything in one pass: it guessed at what the requester wanted, built it all
at once, and nothing checked its work before the requester saw it. We want:

- help deciding *what* to build, and a plan small enough to finish;
- the build done in phases, each ending in something the requester can try;
- tests, verification and a code-quality check before each preview.

Two kagent v1.0.1 facts shape the design:

- **Shared subagents only.** An AgentTemplate can bind another AgentTemplate
  as a `subAgent` tool. Under the default (ADK) harness kagent compiles it
  into the parent's runtime as an ADK sub-agent with its own prompt, model
  and MCP tool bindings. Dedicated subagents, which run as their own
  Session, are not available yet (`agentRef` is commented out in the API).
- **One level deep.** A Shared subagent cannot have Shared subagents of its
  own ("consecutive Shared AgentTemplate tools exceed the kagent runtime
  boundary"), and a template may appear once in the tree.

ADK sub-agents hand over the conversation with `transfer_to_agent`, rather
than being called and returning a value. The agent that spoke last answers
the requester's next message, and a sub-agent may transfer back to its
parent. Approvals resume at the agent that made the paused call.

The web app has no agent picker: every conversation starts with the default
agent.

## Decision

**One conversation, one lead agent with helpers.** The builder remains the
default agent and the requester's single point of contact. It has three
helpers, each a Shared subagent on the same Gemini model:

| Helper | Job | Tools |
| --- | --- | --- |
| `planner` | Turns an idea into a short plan the requester agrees to: who it is for, what they see and enter, what is private, the template, two to four phases. Each phase lists what it builds, what the requester can try, and testable checks. | none |
| `tester` | Adds tests for the current phase's checks, runs the app's lint, format check and tests in Cloud Build, and reports. Changes test files only. | GitLab read and commit; `build_run_checks`, `build_get_status` |
| `reviewer` | Reads the phase's code and reports must-fix problems (security and privacy, unmet checks, breakage) and suggestions. | GitLab read only |

The builder transfers to the planner for any new app, and the planner keeps
the conversation until the requester agrees to the plan. The builder then
commits it as `PLAN.md`, which tracks each phase's status, so a later
conversation can continue the app. For each phase the builder:

1. writes the code;
2. hands over to the tester, and fixes the app until the checks pass;
3. hands over to the reviewer, and fixes what must be fixed;
4. builds the image and deploys the test version (with approval, as
   before);
5. stops for the requester to try it before the next phase.

Each helper's tools are bound narrowly. Only the builder can deploy, and
only through the approval-gated Cloud Run binding. The reviewer cannot write
at all.

**`build_run_checks`** is new in build-mcp. It runs the project's CI checks
at a commit in Cloud Build, one step per check, with the same images as the
templates' GitLab CI: uv for the Python templates, Node for the React
template. `build_get_status` now returns the end of the build's log and the
failed step. That gives agents with no shell real test output, and gives the
builder the actual compiler or Docker error when an image build fails.

## The rest of the roster

| Agent | When | What it needs first |
| --- | --- | --- |
| Discovery / Definition | Folded into the planner for now. Split out when planning grows: finding existing apps that already do it, or reading the requester's spreadsheet. | Catalogue search tool (`AppService`); attachments in the web app |
| Verifier | Opens the deployed test version and walks the phase's "You can try" steps. | A service identity allowed through IAP, and an HTTP or browser tool. Today the checks run against code, not the running app. |
| Concierge | Routes a conversation to the builder, an analyst, or "ask a person". Needs to be the root, but the builder already uses the one level of Shared subagents. | Dedicated subagents (kagent `agentRef`) or a web agent picker |
| Access & sign-in | Grants preview access and explains IAP. | Today `cloudrun_share_preview` covers it. |
| Data steward | Reviews any app that reads BigQuery or stores personal data. | A reviewer variant with BigQuery schema tools; a policy for what "personal" means |
| Release & operations | Promotion readiness, rollbacks, watching errors after go-live. | Production deployer and Cloud Run logs for production |
| Maintainer | Scheduled: dependency updates and template changes for live apps. | Scheduled runs (kagent ScheduledRunService) |

## Consequences

- Each phase costs more model calls: tester and reviewer runs, and a
  one-to-three-minute check build. In exchange, previews arrive tested and
  reviewed, and failures come with real output.
- Helpers' messages appear in the requester's conversation. The prompts keep
  them short and plain, and helpers always transfer back.
- If a helper forgets to transfer back, it keeps answering the requester.
  Its prompt says to transfer back after every report. A stuck conversation
  can be restarted, because `PLAN.md` holds the state.
- Progress cards (`workbench_show`) are left out of the prompts until their
  rejections are fixed. Progress is reported in plain messages.
- The Claude-harness pack in `agents/` keeps its own reviewer subagent. Its
  prompts should be brought in line when that harness is deployed.
