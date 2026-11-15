# syntax=docker/dockerfile:1
# Pinned mcp-atlassian stdio server. Built on demand as a ToolHub artifact;
# the default hub image does not carry this tree.
FROM ghcr.io/astral-sh/uv:0.13.0@sha256:cdc6093146eb3ff6a40107b38f008b789e050e77ad87865e381d9917da55a168 AS uv
FROM python:3.15-slim-trixie@sha256:65a65fc7b30b06cb6df4930fd4650697cb23f4c2698ab81fc87e4b031bc36928
ENV PYTHONUNBUFFERED=1 PYTHONDONTWRITEBYTECODE=1 UV_LINK_MODE=copy
COPY --from=uv /uv /usr/local/bin/uv
RUN apt-get update && apt-get install -y --no-install-recommends git ca-certificates && rm -rf /var/lib/apt/lists/*
WORKDIR /opt/mcp-atlassian
RUN git init && git remote add origin https://github.com/sooperset/mcp-atlassian.git && git fetch --depth 1 origin 74bdaa8f1d28783cccfe99f7b4d75e6dc947cf76 && git checkout --detach FETCH_HEAD && uv sync --python /usr/local/bin/python --frozen --no-dev
RUN groupadd -g 10001 agent && useradd -u 10001 -g agent -d /tmp -s /usr/sbin/nologin agent
USER 10001:10001
ENTRYPOINT ["/opt/mcp-atlassian/.venv/bin/mcp-atlassian"]
