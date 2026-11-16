---
name: using-scaffolds
description: How to choose one of the Workbench golden-path scaffolds and turn it into the requester's app - reading scaffold.yaml, copying, renaming, the repository and branch conventions, and what must not change. Use after the clarifying questions and before writing app code.
---

# Using the golden-path scaffolds

Scaffolds live in `$WORKBENCH_SCAFFOLDS_DIR` (baked into your image). Each has a
`scaffold.yaml` manifest, tests that pass, a Dockerfile, CI, and `IAM.md`.
Every app starts from exactly one of them.

## 1. Choose

```sh
for f in "$WORKBENCH_SCAFFOLDS_DIR"/*/scaffold.yaml; do echo "== $f"; cat "$f"; done
```

Match the requester's answers against `whenToUse` / `whenNotToUse`:

| The app mainly... | Scaffold |
| --- | --- |
| collects input, tracks a process, replaces a spreadsheet or email thread | `python-fastapi-cloudrun` |
| shows charts/tables/filters over data already in BigQuery | `streamlit-bigquery` |
| needs a rich interactive UI (inline editing, drag-and-drop, many live controls) | `node-react-cloudrun` |

When two fit, choose the one with less code (`python-fastapi-cloudrun` before
`node-react-cloudrun`). Tell the requester the choice in plain words ("a simple
web page", "a dashboard"), not the scaffold name.

## 2. Create the app

App names: lowercase letters, digits and hyphens, 3-30 characters, starting
with a letter, e.g. `supplier-issues`. This becomes the GitLab project, the
Cloud Run service and the image name.

```sh
APP=supplier-issues
SCAFFOLD=python-fastapi-cloudrun
mkdir -p /data/workspace && cp -r "$WORKBENCH_SCAFFOLDS_DIR/$SCAFFOLD" "/data/workspace/$APP"
cd "/data/workspace/$APP"
# Replace the placeholder name in exactly the files scaffold.yaml lists under
# rename.files (here for python-fastapi-cloudrun):
sed -i "s/workbench-app/$APP/g" app/main.py app/templates/base.html README.md
# The package name in pyproject.toml / package.json stays workbench-app: the
# lockfile records it, so renaming it would mean regenerating the lockfile.
make test          # must pass before you change anything
git init -b main && git add -A && git commit -m "Start $APP from $SCAFFOLD"
```

Do not run a repository-wide replace: lockfiles and vendored files must not change.

Create the GitLab project in `$WORKBENCH_GITLAB_GROUP_URL` with the gitlab
tools, then `git remote add origin <https url>` and `git push -u origin main`.
Call `workbench_register_app` with `{name, description, scaffold, repositoryUrl}`.

## 3. Change it in small steps

- One behaviour per commit, each with a test. Run `make test` after every change.
- Work on a branch (`feature/<short-name>`) and open a merge request; `main`
  is protected and only changes through merged MRs.
- Keep the scaffold's structure: identity module, logging module, health
  endpoint, Dockerfile, `.gitlab-ci.yml`. Change them only to fix a real
  problem, and say why in the commit.
- New dependencies: only if the scaffold cannot reasonably do it, pinned in the
  lockfile (`uv add`, `npm install --save-exact`). No new frameworks.
- Update `README.md` (what the app does, configuration) and `IAM.md` (any role
  the runtime account now needs, on the narrowest resource) as you go.

## Never

- Add a deploy job to CI, or any Cloud Run / gcloud command to the repo.
- Commit secrets, `.env` files with values, service-account keys, or data.
- Turn off the identity check, the CSP, `maximum_bytes_billed`, or tests to make
  something work.
