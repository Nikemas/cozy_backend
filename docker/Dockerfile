# syntax=docker/dockerfile:1
#
# Production image for cozy_backend (see docs/Cozy — Технический ТЗ для
# разработчика.md §11 "Деплой и инфраструктура"). Build from the repo root:
#
#   docker build -f docker/Dockerfile -t cozy_backend .
#
# This is NOT docker/docker-compose.yml (that one is local-dev-only:
# postgres + minio). This Dockerfile only packages the Go server; wire it
# into a compose file / systemd unit on the VPS with its own env vars
# (DATABASE_URL, JWT_SECRET, ... — see .env.example). No secrets or .env
# files are baked into this image.

# ---- Stage 1: builder -------------------------------------------------
# Version must track the `go` directive in go.mod (currently 1.26.4).
FROM golang:1.26-alpine AS builder

WORKDIR /src

# Download modules first so this layer is cached across source-only changes.
COPY go.mod go.sum ./
RUN go mod download

COPY . .

# CGO_ENABLED=0 -> statically linked binary, no libc dependency, so it can
# run on a distroless "static" base with no shared libraries at all.
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" \
    -o /out/server ./cmd/server

# ---- Stage 2: runtime ---------------------------------------------------
# distroless/static instead of alpine: the binary is fully static (no libc,
# no CGO), the server needs no shell, package manager, or OS utilities at
# runtime, and distroless ships nothing but ca-certificates + tzdata + a
# nonroot user — smallest attack surface and image size for this workload.
# (alpine would work too, but would add a shell/package manager we never
# use.)
FROM gcr.io/distroless/static-debian12:nonroot AS runtime

WORKDIR /app

COPY --from=builder /out/server /app/server

# Shipped for tooling that runs against this image from the outside (e.g. a
# future migrate init-container/job) — the server binary itself does not
# read this directory at startup today.
COPY --from=builder /src/migrations /app/migrations

# internal/web and internal/admin's renderers (render.go in each package)
# call html/template.ParseFiles against these directories by relative path
# at startup (NewRenderer, no go:embed) — WORKDIR is /app, so these must
# land at exactly web/, admin/, locales/ under it to match the paths the
# code expects. Without these, the server fails at boot the moment
# registerWebRoutes/registerAdminRoutes call NewRenderer().
COPY --from=builder /src/web /app/web
COPY --from=builder /src/admin /app/admin
COPY --from=builder /src/locales /app/locales

# Documents intent only — EXPOSE can't reference the HTTP_ADDR env var.
# Default (see internal/config/config.go) is :8080; override HTTP_ADDR at
# runtime if you publish a different port.
EXPOSE 8080

# distroless:nonroot already runs as the nonroot (65532) user; set
# explicitly for clarity/robustness against future base image changes.
USER nonroot:nonroot

ENTRYPOINT ["/app/server"]
