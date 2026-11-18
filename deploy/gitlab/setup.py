#!/usr/bin/env python3
"""Create Workbench's GitLab groups and publish the scaffolds as templates.

    GITLAB_TOKEN=... python3 deploy/gitlab/setup.py --group thehaymaker-workbench

Creates, if missing, a private top-level group with two private subgroups:

    <group>/apps        the builder creates one project per app here
    <group>/templates   one project per scaffold in ../../scaffolds

and publishes every git-tracked file of each scaffold to its template project
in a single commit on main. A template that already has commits is left alone
unless --republish is given, in which case its files are replaced in one new
commit. Idempotent. The token is read from the environment and never printed.

Prints the three paths the GitLab tool server needs, as shell assignments, on
the last lines of output.
"""

import argparse
import base64
import json
import os
import subprocess
import sys
import urllib.error
import urllib.parse
import urllib.request

ROOT = os.path.dirname(os.path.dirname(os.path.dirname(os.path.abspath(__file__))))


class GitLab:
    def __init__(self, base, token):
        self.base = base.rstrip("/") + "/api/v4"
        self.token = token

    def call(self, method, path, body=None, ok=(200, 201)):
        data = json.dumps(body).encode() if body is not None else None
        req = urllib.request.Request(self.base + path, data=data, method=method)
        req.add_header("PRIVATE-TOKEN", self.token)
        req.add_header("Content-Type", "application/json")
        try:
            with urllib.request.urlopen(req, timeout=60) as r:
                raw = r.read()
                return r.status, (json.loads(raw) if raw else None)
        except urllib.error.HTTPError as e:
            raw = e.read()
            try:
                payload = json.loads(raw)
            except ValueError:
                payload = raw.decode(errors="replace")[:300]
            if e.code in ok:
                return e.code, payload
            return e.code, payload

    def get(self, path):
        return self.call("GET", path)


def enc(path):
    return urllib.parse.quote(path, safe="")


def die(msg):
    print(f"error: {msg}", file=sys.stderr)
    sys.exit(1)


def ensure_group(gl, full_path, name, parent=None):
    code, g = gl.get(f"/groups/{enc(full_path)}")
    if code == 200:
        print(f"  group {full_path} exists")
        return g
    body = {"name": name, "path": full_path.rsplit("/", 1)[-1], "visibility": "private"}
    if parent:
        body["parent_id"] = parent["id"]
    code, g = gl.call("POST", "/groups", body)
    if code not in (200, 201):
        hint = ""
        if parent is None:
            hint = (" (gitlab.com may not allow creating top-level groups through the API;"
                    f" create '{full_path}' in the web UI and re-run)")
        die(f"cannot create group {full_path}: HTTP {code} {g}{hint}")
    print(f"  created private group {full_path}")
    return g


def tracked_files(scaffold):
    out = subprocess.run(["git", "-C", ROOT, "ls-files", "-z", f"scaffolds/{scaffold}"],
                         check=True, capture_output=True).stdout
    prefix = f"scaffolds/{scaffold}/"
    return [p.decode()[len(prefix):] for p in out.split(b"\0") if p]


def publish(gl, templates, scaffold, republish):
    path = f"{templates['full_path']}/{scaffold}"
    code, proj = gl.get(f"/projects/{enc(path)}")
    if code != 200:
        code, proj = gl.call("POST", "/projects", {
            "name": scaffold, "path": scaffold, "namespace_id": templates["id"],
            "visibility": "private", "initialize_with_readme": False,
            "default_branch": "main",
            "description": f"Workbench scaffold template ({scaffold}); published from kagent-porcelain/scaffolds",
        })
        if code not in (200, 201):
            die(f"cannot create project {path}: HTTP {code} {proj}")
        print(f"  created project {path}")
    code, branches = gl.get(f"/projects/{proj['id']}/repository/branches")
    has_commits = code == 200 and bool(branches)
    if has_commits and not republish:
        print(f"  {path} already has commits; left alone (use --republish to replace)")
        return

    files = tracked_files(scaffold)
    actions = []
    if has_commits:
        code, tree = gl.get(f"/projects/{proj['id']}/repository/tree?recursive=true&per_page=100&ref=main")
        existing = {t["path"] for t in tree if t["type"] == "blob"} if code == 200 else set()
        for gone in sorted(existing - set(files)):
            actions.append({"action": "delete", "file_path": gone})
    else:
        existing = set()
    for rel in files:
        with open(os.path.join(ROOT, "scaffolds", scaffold, rel), "rb") as f:
            content = base64.b64encode(f.read()).decode()
        actions.append({"action": "update" if rel in existing else "create",
                        "file_path": rel, "content": content, "encoding": "base64"})
    code, commit = gl.call("POST", f"/projects/{proj['id']}/repository/commits", {
        "branch": "main",
        "commit_message": f"Publish the {scaffold} scaffold from kagent-porcelain",
        "actions": actions,
    })
    if code not in (200, 201):
        die(f"cannot commit to {path}: HTTP {code} {commit}")
    print(f"  published {len(files)} files to {path} ({commit['short_id']})")


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--group", required=True, help="top-level group path, e.g. thehaymaker-workbench")
    ap.add_argument("--base-url", default=os.environ.get("GITLAB_BASE_URL", "https://gitlab.com"))
    ap.add_argument("--republish", action="store_true")
    args = ap.parse_args()
    token = os.environ.get("GITLAB_TOKEN")
    if not token:
        die("GITLAB_TOKEN is not set")
    gl = GitLab(args.base_url, token)

    print(f"GitLab at {args.base_url}")
    top = ensure_group(gl, args.group, args.group)
    apps = ensure_group(gl, f"{args.group}/apps", "apps", top)
    templates = ensure_group(gl, f"{args.group}/templates", "templates", top)

    scaffolds = sorted(d for d in os.listdir(os.path.join(ROOT, "scaffolds"))
                       if os.path.isfile(os.path.join(ROOT, "scaffolds", d, "scaffold.yaml")))
    for s in scaffolds:
        publish(gl, templates, s, args.republish)

    print(f"GITLAB_ALLOWED_GROUP={apps['full_path']}")
    print(f"GITLAB_TEMPLATE_GROUP={templates['full_path']}")


if __name__ == "__main__":
    main()
