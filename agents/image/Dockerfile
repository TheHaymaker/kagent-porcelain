# syntax=docker/dockerfile:1.7
#
# Builder images. Build from the REPOSITORY ROOT (the scaffolds are copied in):
#
#   docker buildx build -f agents/image/Dockerfile --target builder \
#     --build-arg KAGENT_CLAUDE_IMAGE=cr.kagent.dev/kagent-dev/kagent/claude-harness@sha256:... \
#     -t europe-docker.pkg.dev/workbench-platform/workbench/builder-harness:$(git rev-parse --short HEAD) --push .
#   docker buildx build -f agents/image/Dockerfile --target toolchain \
#     -t europe-docker.pkg.dev/workbench-platform/workbench/builder-toolchain:$(git rev-parse --short HEAD) --push .
#
# Then pin the printed digests in agents/harness.yaml and agents/sandboxtemplate.yaml.
#
# Deliberately absent: gcloud, docker, kubectl, any cloud SDK credentials.
# The builder changes cloud state only through its MCP tools. Images are built
# by GitLab CI (Kaniko/buildx in the runner), not in the sandbox.

# The kagent Claude harness image for the installed kagent release (alpine,
# runs /usr/local/bin/kagent-claude as uid 65532, Claude Code pinned and
# checksum-verified). Must be digest-pinned; kagent rejects tags.
ARG KAGENT_CLAUDE_IMAGE
ARG ALPINE_IMAGE=alpine:3.24
ARG UV_IMAGE=ghcr.io/astral-sh/uv:0.11.32

FROM ${UV_IMAGE} AS uv

# ---------------------------------------------------------------------------
# toolchain: plain tools image for the SandboxTemplate. kagent replaces the
# entrypoint with its sandbox guest and mounts durable /data.
FROM ${ALPINE_IMAGE} AS toolchain
COPY agents/image/install-toolchain.sh /tmp/install-toolchain.sh
RUN sh /tmp/install-toolchain.sh && rm /tmp/install-toolchain.sh
COPY --from=uv /uv /uvx /usr/local/bin/
COPY scaffolds/ /opt/workbench/scaffolds/
RUN addgroup -S -g 65532 workbench 2>/dev/null || true \
 && adduser -S -D -H -u 65532 -G workbench -h /data -s /bin/bash workbench 2>/dev/null || true
USER 65532:65532
WORKDIR /data

# ---------------------------------------------------------------------------
# builder: the Claude harness image plus the same toolchain. Used as
# Harness.spec.workload.image. The harness entrypoint and user are kept.
FROM ${KAGENT_CLAUDE_IMAGE} AS builder
USER root
COPY agents/image/install-toolchain.sh /tmp/install-toolchain.sh
RUN sh /tmp/install-toolchain.sh && rm /tmp/install-toolchain.sh
COPY --from=uv /uv /uvx /usr/local/bin/
COPY scaffolds/ /opt/workbench/scaffolds/
USER 65532:65532
WORKDIR /data
