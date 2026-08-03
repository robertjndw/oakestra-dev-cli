# syntax=docker/dockerfile:1
# Dockerized Oakestra worker for local testing on macOS.
#
# NodeEngine sources are not part of this repo - they come from the local
# oakestra checkout, passed in as the additional build context 'oakestra-src'
# (see compose/worker.yml). Build manually with:
#   docker build --build-context oakestra-src=../oakestra/go_node_engine worker/

# NETMANAGER_SOURCE picks which of the netmanager-* stages below actually
# builds - BuildKit prunes whichever one isn't reachable from `netmanager`, so
# the "local" stage's reference to the oakestra-net-src context is never
# resolved (and doesn't need to exist) unless it's selected. Default stays
# "release" so a plain `oak-dev up` never requires an oakestra-net checkout.
# Must be declared before the first FROM: an ARG used inside a FROM line has
# to be in the file's global scope, not a build stage's.
ARG NETMANAGER_SOURCE=release

# Stage 1: build NodeEngine from source
FROM golang:1.24-alpine AS builder
WORKDIR /src
COPY --from=oakestra-src go.mod go.sum ./
RUN go mod download
COPY --from=oakestra-src . .
ARG VERSION=dev
# NodeEngine is the CLI (config commands); nodeengined is the actual daemon.
# The CLI starts the daemon via systemctl, which doesn't exist in a container,
# so we run nodeengined directly and use the CLI only for config setup.
RUN CGO_ENABLED=0 GOOS=linux \
    go build -ldflags="-X 'go_node_engine/cmd.Version=${VERSION}'" \
    -o /NodeEngine . && \
    CGO_ENABLED=0 GOOS=linux \
    go build -ldflags="-X 'go_node_engine/cmd.Version=${VERSION}'" \
    -o /nodeengined ./internal/daemon/nodeengined.go

# Stage 2a: fetch NetManager binary from oakestra-net releases (the default).
# Use uname -m inside RUN rather than the TARGETARCH build arg - some Docker
# setups (e.g. OrbStack) do not inject the predefined ARGs automatically, which
# caused the amd64 default to be used on arm64 hosts.
FROM alpine AS netmanager-release
ARG NETMANAGER_VERSION=v0.4.411
RUN apk add --no-cache wget && \
    ARCH=$(uname -m | sed 's/x86_64/amd64/;s/aarch64/arm64/') && \
    echo "[fetch] NetManager_${ARCH} @ ${NETMANAGER_VERSION}" && \
    wget -O /tmp/nm.tar.gz \
      "https://github.com/oakestra/oakestra-net/releases/download/${NETMANAGER_VERSION}/NetManager_${ARCH}.tar.gz" && \
    mkdir /tmp/nm && \
    tar -xzf /tmp/nm.tar.gz -C /tmp/nm && \
    find /tmp/nm -maxdepth 1 -name "NetManager*" ! -name "*.sh" -exec cp {} /NetManager \; && \
    chmod +x /NetManager

# Stage 2b: build NetManager from a local oakestra-net checkout instead -
# selected by compose/override-live-netmanager.yml, which is the only place
# the 'oakestra-net-src' additional context is declared (see
# compose/worker.yml for the always-present 'oakestra-src' equivalent for
# NodeEngine). Static (CGO_ENABLED=0), so unlike the release binary it needs
# no glibc compatibility shim at runtime.
FROM golang:1.24-alpine AS netmanager-local
WORKDIR /src
COPY --from=oakestra-net-src go.mod go.sum ./
RUN go mod download
COPY --from=oakestra-net-src . .
RUN CGO_ENABLED=0 GOOS=linux \
    go build -ldflags="-X 'NetManager/cmd.Version=dev'" -o /NetManager .

FROM netmanager-${NETMANAGER_SOURCE} AS netmanager

# Final image. docker:dind is used as the base because it ships containerd,
# runc, and iptables ready to go - NodeEngine drives containerd directly
# (dockerd itself is never started). socat forwards the cluster/MQTT ports.
# gcompat provides glibc compatibility on Alpine so the glibc-linked NetManager
# release binary runs without needing a glibc-based distro (the locally built
# one doesn't need it, but it's cheap enough to always have).
FROM docker:27-dind
RUN apk add --no-cache socat bash gcompat

COPY --from=builder /NodeEngine /usr/local/bin/NodeEngine
COPY --from=builder /nodeengined /usr/local/bin/nodeengined
COPY --from=netmanager /NetManager /usr/local/bin/NetManager
COPY docker-entrypoint.sh /usr/local/bin/oakestra-entrypoint.sh
RUN chmod +x /usr/local/bin/oakestra-entrypoint.sh

ENTRYPOINT ["/usr/local/bin/oakestra-entrypoint.sh"]
