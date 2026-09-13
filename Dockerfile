# syntax=docker/dockerfile:1

# ---- build ----
FROM golang:1.25-alpine AS build
WORKDIR /src

# Dependencies first, so code changes don't invalidate the module download layer.
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download

COPY . .
ARG VERSION=dev
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=linux go build -trimpath \
      -ldflags="-s -w -X main.version=${VERSION}" \
      -o /out/wallet ./cmd/wallet

# ---- runtime ----
# distroless/static: no shell, no package manager, ~2 MB base. The :nonroot tag runs as
# uid 65532; USER is repeated explicitly so the intent is visible in this file.
FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/wallet /wallet
USER nonroot:nonroot
EXPOSE 8080

# No curl or wget in distroless, so the binary probes itself.
HEALTHCHECK --interval=10s --timeout=3s --start-period=10s --retries=3 \
  CMD ["/wallet", "-healthcheck"]

ENTRYPOINT ["/wallet"]
