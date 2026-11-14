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
ARG VERSION=dev
RUN test -d "cmd/${CMD}" || (echo "CMD must be one of: $(ls cmd)" >&2; exit 1)
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/app ./cmd/${CMD}

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/app /app
USER nonroot:nonroot
# kagent's chart runs the controller image with --config=/etc/kagent/controller.yaml;
# the Workbench configuration is read from /etc/workbench/workbench.yaml.
ENTRYPOINT ["/app"]
