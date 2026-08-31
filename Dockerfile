# syntax=docker/dockerfile:1
#
# Multi-stage build: compile the static, cgo-free maubase binary (same
# `CGO_ENABLED=0` build as `make build`), then ship just that binary in a
# minimal runtime image. See README.md's "Running it" section for the
# MAUBASE_* env vars this expects, and `maubase init` for scaffolding a
# migrations/ + .env.example for whatever you bind-mount in.

FROM golang:1.27-alpine AS builder
WORKDIR /src

# Cached separately from the full source copy below, so editing
# application code doesn't force re-downloading every module on each
# build.
COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/maubase ./cmd/maubase

# distroless/static, not scratch: maubase makes outbound HTTPS calls
# (Resend email, Google/GitHub OAuth token exchange) that need CA
# certificates, which scratch has none of; this image has them plus
# nothing else. The default (root) variant, not :nonroot — the latter's
# baked-in USER directive means WORKDIR/data below would need to be
# created with matching ownership, which isn't exercised by anything in
# this repo's own CI, so getting that wrong silently would be worse than
# just running as root in what's designed as a small single-node
# deployment to begin with (see README.md's design notes).
FROM gcr.io/distroless/static-debian12
WORKDIR /app

COPY --from=builder /out/maubase /app/maubase

# The default MAUBASE_DB_PATH/MAUBASE_STORAGE_DIR/MAUBASE_MIGRATIONS_DIR
# resolve relative to WORKDIR — mount these so a container
# recreate/redeploy doesn't lose the database, uploaded files, or your
# own application migrations.
VOLUME ["/app/data", "/app/migrations"]

EXPOSE 8080
ENTRYPOINT ["/app/maubase"]
