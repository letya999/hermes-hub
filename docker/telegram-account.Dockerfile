# syntax=docker/dockerfile:1
# Standalone Telegram account MCP. Do not FROM the hub image: that copied
# Chromium/Hermes into every account connector.
FROM ghcr.io/astral-sh/uv:0.12.19@sha256:04d046b13e60d6bcec73cbc5e1cad25d680dea90c8573340950a0ac2d1aef424 AS uv
FROM python:3.13-slim-trixie@sha256:8d9d0b8bcf6506481eae4907c18f5e3e7902e629f5f6d684f9e7c32e85e3ddf0
ENV PYTHONUNBUFFERED=1 PYTHONDONTWRITEBYTECODE=1 UV_LINK_MODE=copy
COPY --from=uv /uv /usr/local/bin/uv
RUN apt-get update && apt-get install -y --no-install-recommends git ca-certificates && rm -rf /var/lib/apt/lists/*
WORKDIR /opt/telegram
RUN git init && git remote add origin https://github.com/chigwell/telegram-mcp.git && git fetch --depth 1 origin c9460f8ded6e2457bd70ebabfad840b58d23645d && git checkout --detach FETCH_HEAD && uv sync --python /usr/local/bin/python --frozen --no-dev --extra proxy
COPY docker/telegram-account-mcp.py /opt/hub/telegram-account-mcp.py
COPY docker/telegram-account-login.py /opt/hub/telegram-account-login.py
RUN groupadd -g 10001 agent && useradd -u 10001 -g agent -d /tmp -s /usr/sbin/nologin agent
USER 10001:10001
ENTRYPOINT ["/opt/telegram/.venv/bin/python", "/opt/hub/telegram-account-mcp.py"]
