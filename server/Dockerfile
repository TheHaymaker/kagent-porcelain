# The Workbench server: kagent's controller with Workbench compiled in.
# Build from the server/ directory: docker build -t workbench-server server
FROM golang:1.27 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION=dev
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w -X github.com/kagent-dev/kagent/go/core/internal/version.Version=${VERSION}" \
    -o /out/workbench ./cmd/workbench

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/workbench /workbench
USER nonroot:nonroot
# kagent's chart runs the controller image with --config=/etc/kagent/controller.yaml;
# the Workbench configuration is read from /etc/workbench/workbench.yaml.
ENTRYPOINT ["/workbench"]
