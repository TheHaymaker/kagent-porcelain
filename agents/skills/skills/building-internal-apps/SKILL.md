---
name: building-internal-apps
description: How to talk with a non-engineer who asked for an internal app - which clarifying questions to ask (and how), how to keep them informed while building, and how to hand over. Use at the start of every build, whenever you need a decision from the requester, and when finishing.
---

# Building internal apps for non-engineers

The requester knows their problem, not software. Your job is to turn a plain
description into a small app that works, without making them learn anything.

## Before you build: ask, briefly

Use the ask-user tool (AskUserQuestion). Workbench renders it as a question
card and the build pauses until they answer, so ask everything you need in
**one** round where you can, and never more than two rounds before starting.

Ask at most four questions. Each must change what you build. Good questions:

| Topic | Ask like this | Not like this |
| --- | --- | --- |
| Users | "Who will use this - just your team, or anyone in the company?" | "What's the auth model?" |
| Core job | "What's the one thing someone should be able to do in it?" | "List the user stories." |
| Data in | "Will people type things in, or should it show data that already exists somewhere? If so, where?" | "What's the data source?" |
| Data out | "Should anyone be able to download or share what's in it?" | "Do you need export endpoints?" |
| Sensitivity | "Will it contain anything personal or confidential, like salaries or customer details?" | "What's the data classification?" |

Offer suggested answers (options) when the likely answers are obvious; always
leave room for their own words. If the request is already clear, ask a single
confirming question that restates the app in one sentence:
"So: a page where your team logs supplier issues and sees the open ones. Right?"

Do not ask about: languages, frameworks, hosting, databases, CI, cost centres.
Those are your decisions (see `using-scaffolds`).

## Decide, then say what you decided

After answers, state the plan in two or three sentences and show it as a
Progress component (see `emitting-components`). Example:

> I'll build a small web page where your team can log supplier issues and see
> the open ones. Only people in the company can open it, and it signs them in
> automatically. You'll get a test version to try in about 15 minutes.

## While building

- Update the Progress surface at every step boundary. Use the step's `detail`
  for a short, plain explanation ("checking it works", "putting it online").
- On failure, mark the step `failed`, say in one line what went wrong in plain
  terms and what you will do next. Never paste stack traces to the requester.
- If you must change the plan (data not accessible, feature not possible in
  the scaffold), stop and ask before continuing.

## Handing over a preview

- Give the link via a DeployPreview component, then say exactly what to try:
  "Open the link, add one issue, and check it appears in the list."
- Ask for feedback in one question: "Anything you'd like changed before we
  make it available to everyone?"

## Language rules

- "test version" (preview), "live version" (production), "your app", "sign in".
- Numbers and dates the way the requester wrote them.
- Never mention tokens, service accounts, containers, revisions, YAML, or
  error codes unless they ask.
- Keep messages under about 80 words; put structure in components, not prose.
