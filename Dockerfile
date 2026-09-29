# syntax=docker/dockerfile:1
# Cross-compiles on the build platform, so multi-arch images build fast.
FROM --platform=$BUILDPLATFORM golang:1.27-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION=dev
ARG TARGETOS TARGETARCH
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o /k8s-guardian ./cmd/k8s-guardian

FROM gcr.io/distroless/static:nonroot
LABEL org.opencontainers.image.source="https://github.com/andronaft/k8s-guardian" \
      org.opencontainers.image.description="AI-powered Kubernetes guardrails: CLI, kubectl plugin and MCP server" \
      org.opencontainers.image.licenses="Apache-2.0"
COPY --from=build /k8s-guardian /usr/local/bin/k8s-guardian
WORKDIR /work
USER nonroot
ENTRYPOINT ["/usr/local/bin/k8s-guardian"]
