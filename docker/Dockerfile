# syntax=docker/dockerfile:1
# Pinned upstream code. Hermes MCP lives here; Telegram/Google/Atlassian Python
# MCP trees are dedicated images, not copies inside every hub service.
FROM golang:1.27.3-bookworm AS go_build
WORKDIR /src
COPY go.mod go.sum ./
COPY services/credential-broker/go.mod ./services/credential-broker/
# Module files belong to an exported layer: GHA does not persist cache mounts.
RUN go mod download && cd services/credential-broker && go mod download
COPY services/credential-broker ./services/credential-broker
COPY cmd ./cmd
COPY internal ./internal
RUN --mount=type=cache,id=hermes-go-build,target=/root/.cache/go-build CGO_ENABLED=0 go build -buildvcs=false -trimpath -ldflags='-s -w' -o /hubctl ./cmd/hubctl
RUN --mount=type=cache,id=hermes-go-build,target=/root/.cache/go-build CGO_ENABLED=0 go build -buildvcs=false -trimpath -ldflags='-s -w' -o /hub-runtime ./cmd/runtime
RUN --mount=type=cache,id=hermes-go-build,target=/root/.cache/go-build CGO_ENABLED=0 go build -buildvcs=false -trimpath -ldflags='-s -w' -o /toolhub ./cmd/toolhub
RUN --mount=type=cache,id=hermes-go-build,target=/root/.cache/go-build CGO_ENABLED=0 go build -buildvcs=false -trimpath -ldflags='-s -w' -o /communication-hub ./cmd/communication
RUN --mount=type=cache,id=hermes-go-build,target=/root/.cache/go-build CGO_ENABLED=0 go build -buildvcs=false -trimpath -ldflags='-s -w' -o /e2e ./cmd/e2e

RUN --mount=type=cache,id=hermes-go-build,target=/root/.cache/go-build CGO_ENABLED=0 go build -buildvcs=false -trimpath -ldflags='-s -w' -o /hub-media ./cmd/hub-media
RUN --mount=type=cache,id=hermes-go-build,target=/root/.cache/go-build cd services/credential-broker && CGO_ENABLED=0 go build -buildvcs=false -trimpath -ldflags='-s -w' -o /credential-broker ./cmd/credential-broker
RUN --mount=type=cache,id=hermes-go-build,target=/root/.cache/go-build CGO_ENABLED=0 go build -buildvcs=false -trimpath -ldflags='-s -w' -o /cellinit ./cmd/cellinit

FROM golang:1.27.3-bookworm AS cliproxy_build
RUN --mount=type=cache,id=cliproxy-go-mod,target=/go/pkg/mod --mount=type=cache,id=cliproxy-go-build,target=/root/.cache/go-build set -e; git init /cliproxy && cd /cliproxy && git remote add origin https://github.com/router-for-me/CLIProxyAPI.git && for i in 1 2 3 4; do if git -c http.version=HTTP/1.1 fetch --depth 1 origin ba7e55836dee959e93ec6d41395865d9ec535086; then break; fi; if [ "$i" = 4 ]; then exit 1; fi; sleep 10; done && git checkout --detach FETCH_HEAD && CGO_ENABLED=0 go build -buildvcs=false -trimpath -ldflags='-s -w' -o /cli-proxy-api ./cmd/server

FROM debian:13.6 AS sqlite_build
RUN apt-get update && apt-get install -y --no-install-recommends build-essential ca-certificates curl && rm -rf /var/lib/apt/lists/*
RUN curl -fsSL --retry 3 https://sqlite.org/2026/sqlite-autoconf-3530400.tar.gz -o /tmp/sqlite.tgz && echo '0e9483900e92cd5de8fd48d16bf9200145a61f7fd5be542a5ac81d8a9516eb9c  /tmp/sqlite.tgz' | sha256sum -c - && tar xzf /tmp/sqlite.tgz -C /tmp && cd /tmp/sqlite-autoconf-3530400 && CFLAGS='-O2 -DSQLITE_ENABLE_FTS5 -DSQLITE_ENABLE_COLUMN_METADATA -DSQLITE_THREADSAFE=1' ./configure --prefix=/opt/sqlite-fixed --disable-static && make -j2 && make install

FROM node:26-bookworm-slim@sha256:cd9f682fa2885cd1056e830424764158570061c59736a1da836bc3d73df095ae AS node
FROM docker:29-cli@sha256:018edbc908e08fcc9dbf029c812c34251e9b4719e6f71ca0e5eae2a987d014ca AS dockercli
FROM ghcr.io/stacklok/toolhive:v0.48.0@sha256:f1adc609e11e5f60fd5a27d23e7a63eb14dcfee13f3c0b2f1741680a64fcea5d AS toolhive
FROM ghcr.io/astral-sh/uv:0.13.0@sha256:cdc6093146eb3ff6a40107b38f008b789e050e77ad87865e381d9917da55a168 AS uv

# Pinned upstream checkout + byte-compiled venv, built once and copied into the
# runtime image. UV_COMPILE_BYTECODE trades a slower build for an image that
# does not pay the compile cost (or attempt writes on the read-only rootfs) at
# every container start.
FROM python:3.13-slim-trixie@sha256:8d9d0b8bcf6506481eae4907c18f5e3e7902e629f5f6d684f9e7c32e85e3ddf0 AS python_build
ENV PYTHONUNBUFFERED=1 UV_LINK_MODE=copy UV_COMPILE_BYTECODE=1
COPY --from=uv /uv /usr/local/bin/uv
RUN sed -i 's|http://deb.debian.org|https://deb.debian.org|g' /etc/apt/sources.list.d/debian.sources && apt-get update && apt-get install -y --no-install-recommends git ca-certificates && rm -rf /var/lib/apt/lists/*
WORKDIR /opt/hermes
COPY docker/aiohttp-requirements.txt /opt/aiohttp-requirements.txt
# The pinned upstream fetch is one long HTTP stream; forcing HTTP/1.1 and
# retrying insulates the build from middleboxes cancelling HTTP/2 streams.
# mcp extra only: messaging/google/voice pull Discord, a second Google client
# and a second Chromium that the hub does not use.
# aiohttp is hash-pinned separately: upstream ships it only inside the
# messaging / slack / matrix extras, but the gateway api_server adapter (the
# hub's session API on :8642) needs it even when messaging platforms stay in
# the Go communication hub.
RUN set -e; git init; git remote add origin https://github.com/nousresearch/hermes-agent.git; for i in 1 2 3 4; do if git -c http.version=HTTP/1.1 fetch --depth 1 origin 869228cab4a8276d3b4c78da9d9939670c47bd0f; then break; fi; if [ "$i" = 4 ]; then exit 1; fi; sleep 10; done; git checkout --detach FETCH_HEAD; \
    git rev-parse HEAD > /opt/hermes/.hermes-rev; \
    uv sync --python /usr/local/bin/python --frozen --no-dev --extra mcp; \
    uv pip install --python .venv/bin/python --require-hashes -r /opt/aiohttp-requirements.txt; \
    .venv/bin/python -c "import sqlite3; assert sqlite3.sqlite_version_info >= (3,53,4)"; \
    rm -rf .git tests apps website ui-tui web evals nix docs .github tests-js mcp-research-data datagen-config-examples docker package-lock.json

# hub-core (SPEC-0082 D1): every non-agent daemon shares this lean base — the
# static Go binaries plus TLS roots. No interpreters, browsers or uv: the
# image-budget probe fails the image if any appear.
FROM debian:13.6-slim AS core
RUN sed -i 's|http://deb.debian.org|https://deb.debian.org|g' /etc/apt/sources.list.d/debian.sources && apt-get update && apt-get install -y --no-install-recommends ca-certificates && rm -rf /var/lib/apt/lists/*
COPY --from=go_build /hubctl /hub-runtime /toolhub /communication-hub /credential-broker /hub-media /cellinit /usr/local/bin/
COPY --from=cliproxy_build /cli-proxy-api /usr/local/bin/cli-proxy-api
COPY docker/seccomp-buildkit-rootless.json docker/seccomp-mcp-runtime.json /opt/hub/seccomp/
RUN ln -s /usr/local/bin/communication-hub /usr/local/bin/hub-communication && ln -s /usr/local/bin/toolhub /usr/local/bin/hub-toolhub && ln -s /usr/local/bin/credential-broker /usr/local/bin/hub-credential-broker
ENV HOME=/state/home
RUN groupadd -g 10001 agent && useradd -u 10001 -g agent -d /state/home -s /bin/bash agent && mkdir -p /state /workspace /archive /data && chown 10001:10001 /state /workspace /archive /data
RUN install -d -o 10001 -g 10001 -m 0700 /var/lib/credential-broker-recovery /var/lib/credential-broker-recovery-key
RUN find / -xdev -type f -perm -4000 -exec chmod u-s {} +
WORKDIR /workspace
USER 10001:10001
ENTRYPOINT ["hub-runtime"]
CMD ["idle"]

# Only ToolHub and the workload controller need Docker and ToolHive.
# cellinit ships here so the controller can seed it into the state root and
# bind-mount it into sandbox cells as their PID 1 / canary agent.
FROM core AS control
USER 0:0
COPY --from=dockercli /usr/local/bin/docker /usr/local/bin/docker
COPY --from=toolhive /ko-app/thv /usr/local/bin/thv
USER 10001:10001

# hub-runtime: the agent's Python world — pruned upstream checkout, compiled
# venv and Node for stdio MCP. Voice lives in dedicated services (SPEC-0082
# R2); the browser display stack moves to the per-space sidecar (R4).
FROM python:3.13-slim-trixie@sha256:8d9d0b8bcf6506481eae4907c18f5e3e7902e629f5f6d684f9e7c32e85e3ddf0 AS runtime
# PYTHONDONTWRITEBYTECODE stays set: python_build already compiled the venv,
# and nothing may write .pyc into the writable mounts at runtime.
ENV PYTHONUNBUFFERED=1 PYTHONDONTWRITEBYTECODE=1
RUN sed -i 's|http://deb.debian.org|https://deb.debian.org|g' /etc/apt/sources.list.d/debian.sources && apt-get update && apt-get install -y --no-install-recommends glab && rm -rf /var/lib/apt/lists/*
# Disable pipelining for Docker Desktop paths that stall long download queues.
RUN printf 'Acquire::http::Pipeline-Depth "0";\nAcquire::https::Pipeline-Depth "0";\n' > /etc/apt/apt.conf.d/99-no-pipeline
RUN apt-get update && apt-get install -y --no-install-recommends git ca-certificates curl ripgrep ffmpeg && rm -rf /var/lib/apt/lists/*
COPY --from=sqlite_build /opt/sqlite-fixed/lib/ /usr/local/lib/
RUN apt-get update && apt-get install -y --no-install-recommends poppler-utils && rm -rf /var/lib/apt/lists/*
RUN ldconfig && python -c "import sqlite3; assert sqlite3.sqlite_version_info >= (3,53,4)"
COPY --from=node /usr/local/bin/node /usr/local/bin/node
COPY --from=node /usr/local/lib/node_modules /usr/local/lib/node_modules
RUN ln -s ../lib/node_modules/npm/bin/npm-cli.js /usr/local/bin/npm && ln -s ../lib/node_modules/npm/bin/npx-cli.js /usr/local/bin/npx
COPY --from=python_build /opt/hermes /opt/hermes
# Runtime services exec only these Go entrypoints; the daemons live in core.
COPY --from=go_build /hubctl /hub-runtime /usr/local/bin/
COPY docker/hub-sidecar /usr/local/bin/hub-sidecar
RUN chmod 0755 /usr/local/bin/hub-sidecar
ENV PATH="/opt/hermes/.venv/bin:${PATH}" HERMES_HOME=/state/hermes HOME=/state/home MCPGODEBUG=allowsessionsinstateless=1
RUN groupadd -g 10001 agent && useradd -u 10001 -g agent -d /state/home -s /bin/bash agent && mkdir -p /state /workspace /archive /data && chown 10001:10001 /state /workspace /archive /data
RUN find / -xdev -type f -perm -4000 -exec chmod u-s {} +
WORKDIR /workspace
USER 10001:10001
ENTRYPOINT ["hub-runtime"]
CMD ["idle"]

FROM python:3.13-slim-trixie@sha256:8d9d0b8bcf6506481eae4907c18f5e3e7902e629f5f6d684f9e7c32e85e3ddf0 AS browser
# hub-browser (SPEC-0082 R4, ADR-0083 D3): the per-space sidecar owning the
# headed-Chromium display stack. CDP binds the container interfaces and is
# reachable only on the space-internal network; the sole host-published
# surface is authenticated noVNC on loopback. Python stays: upstream's Meet
# NodeServer is Python and websockify bridges noVNC's WebSocket.
ENV PYTHONUNBUFFERED=1 PYTHONDONTWRITEBYTECODE=1
RUN sed -i 's|http://deb.debian.org|https://deb.debian.org|g' /etc/apt/sources.list.d/debian.sources && apt-get update && apt-get install -y --no-install-recommends ca-certificates curl chromium xvfb fluxbox x11vnc websockify x11-utils && rm -rf /var/lib/apt/lists/*
# R1.4: vendored noVNC tarball — the distro package drags a JS runtime in.
RUN curl -fsSL --retry 3 https://github.com/novnc/noVNC/archive/refs/tags/v1.7.0.tar.gz -o /tmp/novnc.tgz && echo 'b1003a11b6e6e8d8f7f5e5586daae7f8ca651d8aee0aa155ff9ac841c48f52c6  /tmp/novnc.tgz' | sha256sum -c - && tar xzf /tmp/novnc.tgz -C /opt && mv /opt/noVNC-1.7.0 /opt/novnc && rm /tmp/novnc.tgz
COPY --from=node /usr/local/bin/node /usr/local/bin/node
COPY --from=node /usr/local/lib/node_modules /usr/local/lib/node_modules
RUN ln -s ../lib/node_modules/npm/bin/npm-cli.js /usr/local/bin/npm && ln -s ../lib/node_modules/npm/bin/npx-cli.js /usr/local/bin/npx
COPY docker/browser/package*.json /opt/browser/
RUN set -e; cd /opt/browser; for i in 1 2 3 4; do if npm ci --ignore-scripts; then break; fi; if [ "$i" = 4 ]; then exit 1; fi; sleep 10; done; npm cache clean --force
# The Meet node is upstream Python: a hash-pinned venv with playwright +
# websockets, plus the plugin tree it imports — nothing else of the checkout.
COPY docker/meet-requirements.txt /opt/meet/requirements.txt
RUN python -m venv /opt/meet/.venv && /opt/meet/.venv/bin/pip install --no-cache-dir --require-hashes -r /opt/meet/requirements.txt && /opt/meet/.venv/bin/python -m playwright install --with-deps chromium && rm -rf /var/lib/apt/lists/*
COPY --from=python_build /opt/hermes/hermes_constants.py /opt/hermes/
COPY --from=python_build /opt/hermes/plugins/google_meet /opt/hermes/plugins/google_meet
COPY --from=go_build /hub-runtime /usr/local/bin/
ENV HOME=/state/home
RUN groupadd -g 10001 agent && useradd -u 10001 -g agent -d /state/home -s /bin/bash agent && mkdir -p /state /workspace && chown 10001:10001 /state /workspace
RUN find / -xdev -type f -perm -4000 -exec chmod u-s {} +
WORKDIR /workspace
USER 10001:10001
ENTRYPOINT ["hub-runtime"]
CMD ["browser"]

# Bounded-CLI cells run on this dedicated image: catalog commands (git, rg)
# plus the static cellinit helper, and nothing else — no /state, no socket,
# no interpreters that could execute mutable workspace code.
FROM debian:13.6-slim AS cli-tools
# libatomic1 lets mounted member images run their embedded node runtime
# (eslint/tsc/prettier artifacts) inside a toolbox or tool-bearing shell cell.
RUN apt-get update && apt-get install -y --no-install-recommends git ripgrep ca-certificates libatomic1 && rm -rf /var/lib/apt/lists/*
COPY --from=go_build /cellinit /cellinit
# uv is the package-install surface for install_packages cells: it writes
# into the private /cellhome paths ahead of the image PATH and reaches only
# the cell's declared egress hosts.
COPY --from=uv /uv /uvx /usr/local/bin/
USER 10001:10001
ENTRYPOINT ["/cellinit"]
ARG GIT_SHA=unknown
LABEL org.opencontainers.image.revision="${GIT_SHA}"

# Egress companion for cells that declare reviewed outbound hosts: a plain
# Squid forward proxy the controller scopes to the cell's internal network.
FROM debian:13.6-slim AS cell-proxy
RUN apt-get update && apt-get install -y --no-install-recommends squid ca-certificates && rm -rf /var/lib/apt/lists/*
EXPOSE 3128
USER 10001:10001
CMD ["squid", "-N", "-f", "/etc/squid/squid.conf"]
ARG GIT_SHA=unknown
LABEL org.opencontainers.image.revision="${GIT_SHA}"

# Immutable deployment target; no source tree or Go compiler.
FROM runtime AS prod
LABEL org.opencontainers.image.licenses="AGPL-3.0-only"
ARG GIT_SHA=unknown
LABEL org.opencontainers.image.revision="${GIT_SHA}"

# Developer target: same agent runtime plus a mounted /src working tree.
# The Go toolchain stays on the host; copying it into every service image
# duplicated ~280MB on each rebuild. The e2e harness ships only here.
FROM runtime AS dev
USER 0:0
COPY --from=go_build /e2e /usr/local/bin/e2e
RUN mkdir /src && chown 10001:10001 /src
USER 10001:10001
ARG GIT_SHA=unknown
LABEL org.opencontainers.image.revision="${GIT_SHA}"

FROM core AS prod-core
LABEL org.opencontainers.image.licenses="AGPL-3.0-only"
ARG GIT_SHA=unknown
LABEL org.opencontainers.image.revision="${GIT_SHA}"

FROM core AS dev-core
USER 0:0
RUN mkdir /src && chown 10001:10001 /src
USER 10001:10001
ARG GIT_SHA=unknown
LABEL org.opencontainers.image.revision="${GIT_SHA}"

FROM control AS prod-control
LABEL org.opencontainers.image.licenses="AGPL-3.0-only"
ARG GIT_SHA=unknown
LABEL org.opencontainers.image.revision="${GIT_SHA}"

FROM control AS dev-control
USER 0:0
RUN mkdir /src && chown 10001:10001 /src
USER 10001:10001
ARG GIT_SHA=unknown
LABEL org.opencontainers.image.revision="${GIT_SHA}"

FROM browser AS prod-browser
LABEL org.opencontainers.image.licenses="AGPL-3.0-only"
ARG GIT_SHA=unknown
LABEL org.opencontainers.image.revision="${GIT_SHA}"

FROM browser AS dev-browser
USER 0:0
RUN mkdir /src && chown 10001:10001 /src
USER 10001:10001
ARG GIT_SHA=unknown
LABEL org.opencontainers.image.revision="${GIT_SHA}"
