# ── Stage 1: Build ────────────────────────────────────────────────────────────
FROM golang:1.25-alpine AS builder

WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build \
    -trimpath \
    -ldflags "-s -w" \
    -o /jelly-diff ./cmd/jelly-diff

# ── Stage 2: Runtime ──────────────────────────────────────────────────────────
FROM alpine:3.20

RUN apk add --no-cache tzdata ca-certificates

ARG PUID=1000
ARG PGID=1000

# Create group + user with the requested UID/GID so /data files are owned correctly
RUN addgroup -g ${PGID} jellyuser && \
    adduser -D -u ${PUID} -G jellyuser jellyuser

WORKDIR /app
COPY --from=builder /jelly-diff /app/jelly-diff

# Create data dir and hand ownership to the app user
RUN mkdir -p /data && chown jellyuser:jellyuser /data

USER jellyuser

EXPOSE 6363
VOLUME ["/data"]

HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 \
  CMD wget -qO- http://localhost:6363/health || exit 1

ENTRYPOINT ["/app/jelly-diff"]
