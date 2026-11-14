# syntax=docker/dockerfile:1
# One image per tool server, selected with --build-arg SERVER=<name>:
#   docker build --build-arg SERVER=gitlab-mcp -t .../gitlab-mcp:TAG tools/
# SERVER is one of: gitlab-mcp, cloudrun-mcp, artifactregistry-mcp, bigquery-mcp.
ARG GO_VERSION=1.26

FROM golang:${GO_VERSION} AS build
ARG SERVER
ARG VERSION=dev
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN test -n "${SERVER}" && test -d "cmd/${SERVER}" || (echo "set --build-arg SERVER to one of: $(ls cmd)" >&2; exit 1)
RUN CGO_ENABLED=0 go build -trimpath \
      -ldflags "-s -w -X github.com/thehaymaker/kagent-porcelain/tools/internal/app.Version=${VERSION}" \
      -o /out/server ./cmd/${SERVER}

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/server /server
USER nonroot:nonroot
EXPOSE 8080
ENTRYPOINT ["/server"]
