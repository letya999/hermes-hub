# syntax=docker/dockerfile:1
# Pinned upstream code. Hermes MCP lives here; Telegram/Google/Atlassian Python
# MCP trees are dedicated images, not copies inside every hub service.
FROM golang:1.27.1-bookworm AS go_build
WORKDIR /src
COPY go.mod go.sum ./
COPY services/credential-broker ./services/credential-broker
RUN go mod download
RUN git init /slack && cd /slack && git remote add origin https://github.com/korotovsky/slack-mcp-server.git && git fetch --depth 1 origin b88c0de3f706f4f07337c9eda7133c736d1c9524 && git checkout --detach FETCH_HEAD && CGO_ENABLED=0 go build -buildvcs=false -trimpath -o /slack-mcp-server ./cmd/slack-mcp-server
RUN git init /cliproxy && cd /cliproxy && git remote add origin https://github.com/router-for-me/CLIProxyAPI.git && git fetch --depth 1 origin ba7e55836dee959e93ec6d41395865d9ec535086 && git checkout --detach FETCH_HEAD && CGO_ENABLED=0 go build -buildvcs=false -trimpath -ldflags='-s -w' -o /cli-proxy-api ./cmd/server
COPY cmd ./cmd
COPY internal ./internal
RUN CGO_ENABLED=0 go build -buildvcs=false -trimpath -ldflags='-s -w' -o /hubctl ./cmd/hubctl
RUN CGO_ENABLED=0 go build -buildvcs=false -trimpath -ldflags='-s -w' -o /hub-runtime ./cmd/runtime
RUN CGO_ENABLED=0 go build -buildvcs=false -trimpath -ldflags='-s -w' -o /toolhub ./cmd/toolhub
RUN CGO_ENABLED=0 go build -buildvcs=false -trimpath -ldflags='-s -w' -o /communication-hub ./cmd/communication
RUN cd services/credential-broker && CGO_ENABLED=0 go build -buildvcs=false -trimpath -ldflags='-s -w' -o /credential-broker ./cmd/credential-broker

FROM debian:13.6 AS sqlite_build
RUN apt-get update && apt-get install -y --no-install-recommends build-essential ca-certificates curl && rm -rf /var/lib/apt/lists/*
RUN curl -fsSL --retry 3 https://sqlite.org/2026/sqlite-autoconf-3530400.tar.gz -o /tmp/sqlite.tgz && echo '0e9483900e92cd5de8fd48d16bf9200145a61f7fd5be542a5ac81d8a9516eb9c  /tmp/sqlite.tgz' | sha256sum -c - && tar xzf /tmp/sqlite.tgz -C /tmp && cd /tmp/sqlite-autoconf-3530400 && CFLAGS='-O2 -DSQLITE_ENABLE_FTS5 -DSQLITE_ENABLE_COLUMN_METADATA -DSQLITE_THREADSAFE=1' ./configure --prefix=/opt/sqlite-fixed --disable-static && make -j2 && make install

FROM node:26-bookworm-slim@sha256:cd9f682fa2885cd1056e830424764158570061c59736a1da836bc3d73df095ae AS node
FROM docker:29-cli@sha256:018edbc908e08fcc9dbf029c812c34251e9b4719e6f71ca0e5eae2a987d014ca AS dockercli
FROM ghcr.io/stacklok/toolhive:v0.51.2@sha256:a8a5c88a7d8892c793db49031986db0f209e06c40605adc8fcf34f4b12ad573d AS toolhive
FROM ghcr.io/astral-sh/uv:0.12.13@sha256:b485bd65cc2cf1c9a93b3554012c9c3778cf7b1b5fd3d3096ce9e1226c97e1e6 AS uv
FROM python:3.13-slim-trixie@sha256:8d9d0b8bcf6506481eae4907c18f5e3e7902e629f5f6d684f9e7c32e85e3ddf0 AS runtime
ENV PYTHONUNBUFFERED=1 PYTHONDONTWRITEBYTECODE=1 UV_LINK_MODE=copy
COPY --from=uv /uv /usr/local/bin/uv
RUN apt-get update && apt-get install -y --no-install-recommends glab
RUN apt-get update && apt-get install -y --no-install-recommends git ca-certificates curl ripgrep ffmpeg chromium xvfb fluxbox x11vnc novnc websockify && rm -rf /var/lib/apt/lists/*
COPY --from=sqlite_build /opt/sqlite-fixed/lib/ /usr/local/lib/
RUN ldconfig && python -c "import sqlite3; assert sqlite3.sqlite_version_info >= (3,53,4)"
COPY --from=node /usr/local/bin/node /usr/local/bin/node
COPY --from=node /usr/local/lib/node_modules /usr/local/lib/node_modules
RUN ln -s ../lib/node_modules/npm/bin/npm-cli.js /usr/local/bin/npm && ln -s ../lib/node_modules/npm/bin/npx-cli.js /usr/local/bin/npx
WORKDIR /opt/hermes
# mcp extra only: messaging/google/voice pull Discord, a second Google client,
# faster-whisper and a second Chromium that the hub does not use. Browser CDP
# uses apt Chromium; Meet/STT stay out of the default image.
# Google Workspace, Atlassian and Telegram-account Python trees are dedicated
# images (docker/telegram-account.Dockerfile, docker/mcp-atlassian.Dockerfile)
# and ToolHub artifacts, not copies inside every hub service.
# aiohttp is pinned separately: upstream ships it only inside the messaging /
# slack / matrix extras, but the gateway api_server adapter (the hub's session
# API on :8642) needs it even when messaging platforms stay in the Go
# communication hub.
RUN git init && git remote add origin https://github.com/nousresearch/hermes-agent.git && git fetch --depth 1 origin 869228cab4a8276d3b4c78da9d9939670c47bd0f && git checkout --detach FETCH_HEAD && uv sync --python /usr/local/bin/python --frozen --no-dev --extra mcp && uv pip install --python .venv/bin/python aiohttp==3.14.3 && .venv/bin/python -c "import sqlite3; assert sqlite3.sqlite_version_info >= (3,53,4)"
COPY docker/browser/package*.json /opt/browser/
RUN cd /opt/browser && npm ci --ignore-scripts && npm cache clean --force
COPY --from=go_build /hubctl /hub-runtime /toolhub /communication-hub /credential-broker /slack-mcp-server /cli-proxy-api /usr/local/bin/
COPY --from=dockercli /usr/local/bin/docker /usr/local/bin/docker
COPY --from=toolhive /ko-app/thv /usr/local/bin/thv
COPY docker/seccomp-buildkit-rootless.json docker/seccomp-mcp-runtime.json /opt/hub/seccomp/
COPY docker/hub-stt /usr/local/bin/hub-stt
RUN chmod 0755 /usr/local/bin/hub-stt
RUN ln -s /usr/local/bin/communication-hub /usr/local/bin/hub-communication && ln -s /usr/local/bin/toolhub /usr/local/bin/hub-toolhub && ln -s /usr/local/bin/credential-broker /usr/local/bin/hub-credential-broker
COPY --from=go_build /slack/LICENSE /usr/share/licenses/slack-mcp-server/LICENSE
ENV PATH="/opt/hermes/.venv/bin:${PATH}" DISPLAY=:99 HERMES_HOME=/state/hermes HOME=/state/home MCPGODEBUG=allowsessionsinstateless=1
RUN groupadd -g 10001 agent && useradd -u 10001 -g agent -d /state/home -s /bin/bash agent && mkdir -p /state /workspace /archive && chown 10001:10001 /state /workspace /archive
WORKDIR /workspace
USER 10001:10001
ENTRYPOINT ["hub-runtime"]
CMD ["idle"]

# Immutable deployment target; no source tree or Go compiler.
FROM runtime AS prod
LABEL org.opencontainers.image.licenses="AGPL-3.0-only"

# Developer target: same agent runtime plus a mounted /src working tree.
# The Go toolchain stays on the host; copying it into every service image
# duplicated ~280MB on each rebuild.
FROM runtime AS dev
USER 0:0
RUN mkdir /src && chown 10001:10001 /src
USER 10001:10001
