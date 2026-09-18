# Pinned upstream code, isolated Python environments (Hermes MCP 2, Telegram MCP 1).
FROM golang:1.27.1-bookworm AS go_build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
RUN CGO_ENABLED=0 go build -buildvcs=false -trimpath -ldflags='-s -w' -o /hubctl ./cmd/hubctl
RUN CGO_ENABLED=0 go build -buildvcs=false -trimpath -ldflags='-s -w' -o /hub-runtime ./cmd/runtime
RUN CGO_ENABLED=0 go build -buildvcs=false -trimpath -ldflags='-s -w' -o /communication-hub ./cmd/communication
RUN git init /slack && cd /slack && git remote add origin https://github.com/korotovsky/slack-mcp-server.git && git fetch --depth 1 origin b88c0de3f706f4f07337c9eda7133c736d1c9524 && git checkout --detach FETCH_HEAD && CGO_ENABLED=0 go build -buildvcs=false -trimpath -o /slack-mcp-server ./cmd/slack-mcp-server

FROM debian:13.4 AS sqlite_build
RUN apt-get update && apt-get install -y --no-install-recommends build-essential ca-certificates curl && rm -rf /var/lib/apt/lists/*
RUN curl -fsSL --retry 3 https://sqlite.org/2026/sqlite-autoconf-3530400.tar.gz -o /tmp/sqlite.tgz && echo '0e9483900e92cd5de8fd48d16bf9200145a61f7fd5be542a5ac81d8a9516eb9c  /tmp/sqlite.tgz' | sha256sum -c - && tar xzf /tmp/sqlite.tgz -C /tmp && cd /tmp/sqlite-autoconf-3530400 && CFLAGS='-O2 -DSQLITE_ENABLE_FTS5 -DSQLITE_ENABLE_COLUMN_METADATA -DSQLITE_THREADSAFE=1' ./configure --prefix=/opt/sqlite-fixed --disable-static && make -j2 && make install

FROM node:26-bookworm-slim@sha256:9e6f9357d371591e32ab6f2d8a26d63bdd0d17c29eee3f4f3e7e454d9634bf73 AS node
FROM ghcr.io/astral-sh/uv:0.12.17-python3.13-trixie@sha256:b49f54e077b975791900368b1c51f8ce2e139f0eba89d4a2dc0cad6d614d8736 AS runtime
ENV PYTHONUNBUFFERED=1 PYTHONDONTWRITEBYTECODE=1 UV_LINK_MODE=copy PLAYWRIGHT_BROWSERS_PATH=/opt/playwright
RUN apt-get update && apt-get install -y --no-install-recommends glab
RUN apt-get update && apt-get install -y --no-install-recommends git ca-certificates curl ripgrep ffmpeg chromium xvfb fluxbox x11vnc novnc websockify libportaudio2 build-essential libffi-dev libolm-dev && rm -rf /var/lib/apt/lists/*
COPY --from=sqlite_build /opt/sqlite-fixed/lib/ /usr/local/lib/
RUN ldconfig && python -c "import sqlite3; assert sqlite3.sqlite_version_info >= (3,53,4)"
COPY --from=node /usr/local/bin/node /usr/local/bin/node
COPY --from=node /usr/local/lib/node_modules /usr/local/lib/node_modules
RUN ln -s ../lib/node_modules/npm/bin/npm-cli.js /usr/local/bin/npm && ln -s ../lib/node_modules/npm/bin/npx-cli.js /usr/local/bin/npx
WORKDIR /opt/hermes
RUN git init && git remote add origin https://github.com/nousresearch/hermes-agent.git && git fetch --depth 1 origin 869228cab4a8276d3b4c78da9d9939670c47bd0f && git checkout --detach FETCH_HEAD && uv sync --python /usr/local/bin/python --frozen --no-dev --extra mcp --extra messaging --extra google --extra voice
COPY docker/meet-requirements.txt /opt/hub/meet-requirements.txt
RUN uv pip install --python /opt/hermes/.venv/bin/python --no-config --no-deps --require-hashes -r /opt/hub/meet-requirements.txt && .venv/bin/python -m playwright install --with-deps chromium && .venv/bin/python -c "import sqlite3; assert sqlite3.sqlite_version_info >= (3,53,4)"
WORKDIR /opt/telegram
RUN git init && git remote add origin https://github.com/chigwell/telegram-mcp.git && git fetch --depth 1 origin c9460f8ded6e2457bd70ebabfad840b58d23645d && git checkout --detach FETCH_HEAD && uv sync --python /usr/local/bin/python --frozen --no-dev
WORKDIR /opt/google
RUN git init && git remote add origin https://github.com/taylorwilsdon/google_workspace_mcp.git && git fetch --depth 1 origin 54b1c56f7f9912ce32681460d7ca38f9c2a37564 && git checkout --detach FETCH_HEAD && uv sync --python /usr/local/bin/python --frozen --no-dev
WORKDIR /opt/mcp-atlassian
RUN git init && git remote add origin https://github.com/sooperset/mcp-atlassian.git && git fetch --depth 1 origin 74bdaa8f1d28783cccfe99f7b4d75e6dc947cf76 && git checkout --detach FETCH_HEAD && uv sync --python /usr/local/bin/python --frozen --no-dev
COPY docker/browser/package*.json /opt/browser/
RUN cd /opt/browser && npm ci --ignore-scripts && npm cache clean --force
COPY --from=go_build /hubctl /hub-runtime /communication-hub /slack-mcp-server /usr/local/bin/
RUN ln -s /usr/local/bin/communication-hub /usr/local/bin/hub-communication
COPY --from=go_build /slack/LICENSE /usr/share/licenses/slack-mcp-server/LICENSE
ENV PATH="/opt/hermes/.venv/bin:${PATH}" DISPLAY=:99 HERMES_HOME=/state/hermes HOME=/state/home
RUN groupadd -g 10001 agent && useradd -u 10001 -g agent -d /state/home -s /bin/bash agent && mkdir -p /state /workspace /archive && chown 10001:10001 /state /workspace /archive
WORKDIR /workspace
USER 10001:10001
ENTRYPOINT ["hub-runtime"]
CMD ["idle"]

# Immutable deployment target; no source tree or Go compiler.
FROM runtime AS prod
LABEL org.opencontainers.image.licenses="AGPL-3.0-only"

# Developer target: same agent runtime plus Go and a mounted /src working tree.
FROM runtime AS dev
USER 0:0
RUN mkdir /src && chown 10001:10001 /src
COPY --from=go_build /usr/local/go /usr/local/go
ENV PATH="/usr/local/go/bin:${PATH}" GOCACHE=/state/go-build GOMODCACHE=/state/go-mod
USER 10001:10001
