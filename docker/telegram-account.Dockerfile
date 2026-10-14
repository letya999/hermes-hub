# syntax=docker/dockerfile:1
# Standalone Telegram account MCP. Do not FROM the hub image: that copied
# Chromium/Hermes into every account connector.
FROM ghcr.io/astral-sh/uv:0.13.0@sha256:cdc6093146eb3ff6a40107b38f008b789e050e77ad87865e381d9917da55a168 AS uv
FROM python:3.13-slim-trixie@sha256:8d9d0b8bcf6506481eae4907c18f5e3e7902e629f5f6d684f9e7c32e85e3ddf0
ENV PYTHONUNBUFFERED=1 PYTHONDONTWRITEBYTECODE=1 UV_LINK_MODE=copy
COPY --from=uv /uv /usr/local/bin/uv
RUN apt-get update && apt-get install -y --no-install-recommends git ca-certificates && rm -rf /var/lib/apt/lists/*
WORKDIR /opt/telegram
RUN git init && git remote add origin https://github.com/letya999/telegram-mcp.git && git fetch --depth 1 origin 26f1632b2b07cca16fa8f172fe645477921db9ed && git checkout --detach FETCH_HEAD && uv sync --python /usr/local/bin/python --frozen --no-dev --extra proxy
COPY docker/telegram-account-mcp.py /opt/hub/telegram-account-mcp.py
COPY docker/telegram-account-login.py /opt/hub/telegram-account-login.py
RUN groupadd -g 10001 agent && useradd -u 10001 -g agent -d /tmp -s /usr/sbin/nologin agent
USER 10001:10001
ENTRYPOINT ["/opt/telegram/.venv/bin/python", "/opt/hub/telegram-account-mcp.py"]
