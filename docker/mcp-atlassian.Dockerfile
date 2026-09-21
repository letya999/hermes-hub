# syntax=docker/dockerfile:1
# Pinned mcp-atlassian stdio server. Built on demand as a ToolHub artifact;
# the default hub image does not carry this tree.
FROM ghcr.io/astral-sh/uv:0.12.13@sha256:b485bd65cc2cf1c9a93b3554012c9c3778cf7b1b5fd3d3096ce9e1226c97e1e6 AS uv
FROM python:3.13-slim-trixie@sha256:8d9d0b8bcf6506481eae4907c18f5e3e7902e629f5f6d684f9e7c32e85e3ddf0
ENV PYTHONUNBUFFERED=1 PYTHONDONTWRITEBYTECODE=1 UV_LINK_MODE=copy
COPY --from=uv /uv /usr/local/bin/uv
RUN apt-get update && apt-get install -y --no-install-recommends git ca-certificates && rm -rf /var/lib/apt/lists/*
WORKDIR /opt/mcp-atlassian
RUN git init && git remote add origin https://github.com/sooperset/mcp-atlassian.git && git fetch --depth 1 origin 74bdaa8f1d28783cccfe99f7b4d75e6dc947cf76 && git checkout --detach FETCH_HEAD && uv sync --python /usr/local/bin/python --frozen --no-dev
RUN groupadd -g 10001 agent && useradd -u 10001 -g agent -d /tmp -s /usr/sbin/nologin agent
USER 10001:10001
ENTRYPOINT ["/opt/mcp-atlassian/.venv/bin/mcp-atlassian"]
