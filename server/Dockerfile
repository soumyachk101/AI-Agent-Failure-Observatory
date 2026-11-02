# Observatory server — production packaging.
#
# The default Go build (cmd/observatory) runs the dev path end to end:
#   - embedded SQLite via modernc.org/sqlite
#   - file-based durable queue   (internal/queue/local.go)
#   - local object-store dir     (internal/blob/blob.go)
# Production targets (Postgres/ClickHouse/NATS JetStream/S3) are build-tagged
# migration code and are NOT compiled into the default binary.
#
# No CGO is required: the only external dependency is modernc.org/sqlite, a
# pure-Go driver, so the binary is static. We build with CGO_ENABLED=0 and
# ship it on a minimal non-root distroless base.
FROM golang:1.27 AS build
WORKDIR /src
ENV CGO_ENABLED=0
# Match server/go.mod: go 1.27.
COPY go.mod go.sum* ./
RUN go mod download
COPY . .
RUN go build -trimpath -ldflags="-s -w" -o /out/observatory ./cmd/observatory

# gcr.io/distroless/static has a "nonroot" user (uid/gid 65532). The server
# only listens on OBS_ADDR (:8080 by default); it needs no CA bundle for the
# dev path, and no TTY/shell.
FROM gcr.io/distroless/static-debian12 AS runtime
COPY --from=build --chown=nonroot:nonroot /out/observatory /observatory
USER nonroot
EXPOSE 8080
# Runtime config is entirely via flags/env (see cmd/observatory/main.go).
# Defaults: OBS_ADDR=:8080, OBS_DATA_DIR=./data, OBS_LOG_LEVEL=info,
# OBS_SEED_DEMO=true, OBS_WEBHOOK_SECRET="".
ENTRYPOINT ["/observatory"]
