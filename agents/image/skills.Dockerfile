# The builder's skills as an OCI artifact for AgentTemplate.spec.plugins[].source.oci.
# kagent mounts the whole image read-only and validates the package (regular
# files/dirs only, size limits, no escaping symlinks), so build FROM scratch
# with only the package contents:
#
#   docker buildx build -f agents/image/skills.Dockerfile \
#     -t europe-docker.pkg.dev/workbench-platform/workbench/builder-skills:$(git rev-parse --short HEAD) --push agents/skills
#
# then pin the digest in agents/agenttemplates.yaml.
FROM scratch
COPY plugin.json /plugin.json
COPY skills/ /skills/
