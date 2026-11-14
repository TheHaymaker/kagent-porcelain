---
name: requesting-production
description: How to ask for an app to go to production - the readiness checklist, calling workbench_request_promotion with the exact preview revision, showing the ApprovalCard, and what to do while waiting or after a rejection. Use only when the requester says the preview is right.
---

# Requesting production

Production is the live version people across the company use. You cannot put
anything there. You can only ask, with `workbench_request_promotion`; an
approver decides in Workbench, and Workbench (not you) deploys the exact
preview revision that was approved.

## Before asking

All of these must be true. If one is not, fix it or tell the requester why not.

- [ ] The requester said, in their words, that the preview does what they need.
- [ ] The merge request's pipeline passed on the commit the preview runs.
- [ ] The `reviewer` subagent has reviewed the final diff with no blocking findings.
- [ ] `README.md` describes the app and its configuration; `IAM.md` lists every
      role the runtime account needs, scoped narrowly.
- [ ] No secrets, personal data or test data in the repository.
- [ ] For data apps: every query sets maximum bytes billed; the runtime account
      is read-only.
- [ ] `workbench_get_app` shows the preview deployment you are about to promote.

## Ask

```json
{
  "appId": "app_01",
  "revision": "supplier-issues-00007-xid",
  "note": "Team tracker for supplier issues. Reviewed; tests pass. Reads no company data. Requested by the Procurement team lead."
}
```

- `revision` is the preview revision recorded with `workbench_record_deployment`,
  exactly. Approval is for that revision only; if you change anything
  afterwards you must deploy a new preview and ask again.
- `note` is for the approver: what the app does, who asked, what data it
  touches, anything unusual. Three sentences at most.

The result is `{status, approvalId, productionUrl?, message}`:

| `status` | Meaning | What you do |
| --- | --- | --- |
| `pending` | An approval is open; `message` says who decides | Show an ApprovalCard with `approvalId` (see `emitting-components`) and tell the requester: "I've asked for this to go live. <who> will review it; you'll see the decision here." |
| `promoted` | Already approved and deployed (Workbench may have paused this very call for an approver) | Show an AppCard and say it's live, with `productionUrl` |
| `refused` | An approver said no; `message` has the reason | Explain the reason in plain words and offer the fix |

## While waiting / after the decision

- Do not ask again, ping approvers, or look for another route. The ApprovalCard
  updates itself from the ApprovalService.
- To check the outcome, use `workbench_get_app` (`status`, `productionUrl`).
  Calling `workbench_request_promotion` again for the same app and revision
  reports the open approval instead of opening a second one, but do it only
  when the requester asks for an update.
- After a refusal, fix what was asked, deploy a new preview, and ask again for
  the new revision.
