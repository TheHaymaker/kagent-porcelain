# Images built from the server module. CMD selects the command:
#   workbench          the Workbench server (default): kagent's controller
#                      with Workbench compiled in
#   workbench-mockllm  a scripted model for the end-to-end smoke test
# Build from the server/ directory:
#   docker build -t workbench-server server
#   docker build --build-arg CMD=workbench-mockllm -t workbench-mockllm server
FROM golang:1.27 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG CMD=workbench
# The kagent release server/go.mod pins. kagent's controller refuses to start
# when the chart's built-in harness images belong to another release, so the
# embedded controller must report the release it is.
ARG KAGENT_VERSION=v1.0.1
ARG SUBSTRATE_VERSION=1.0.0
RUN test -d "cmd/${CMD}" || (echo "CMD must be one of: $(ls cmd)" >&2; exit 1)
RUN v=github.com/kagent-dev/kagent/go/core/internal/version && \
    CGO_ENABLED=0 go build -trimpath \
      -ldflags="-s -w -X $v.Version=${KAGENT_VERSION} -X $v.SubstrateVersion=${SUBSTRATE_VERSION}" \
      -o /out/app ./cmd/${CMD}

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/app /app
# Numeric, so Kubernetes can verify runAsNonRoot (distroless's nonroot is 65532).
USER 65532:65532
# kagent's chart runs the controller image with --config=/etc/kagent/controller.yaml;
# the Workbench configuration is read from /etc/workbench/workbench.yaml.
ENTRYPOINT ["/app"]
