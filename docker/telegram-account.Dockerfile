# Reuse the repository's already pinned Telegram MCP + Telethon environment.
# Supply a real built hermes-hub image digest, never a mutable tag.
ARG HUB_IMAGE
FROM ${HUB_IMAGE}
USER root
# Enable the upstream's already hash-locked HTTP CONNECT proxy extra.
RUN cd /opt/telegram && uv sync --python /usr/local/bin/python --frozen --no-dev --extra proxy
COPY docker/telegram-account-mcp.py /opt/hub/telegram-account-mcp.py
COPY docker/telegram-account-login.py /opt/hub/telegram-account-login.py
USER 10001:10001
ENTRYPOINT ["/opt/telegram/.venv/bin/python", "/opt/hub/telegram-account-mcp.py"]
