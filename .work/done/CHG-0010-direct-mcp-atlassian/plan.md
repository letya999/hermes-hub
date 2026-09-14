---
change: CHG-0010
title: Replace Atlassian Rovo with direct mcp-atlassian
status: in_progress
---

# Plan

1. Pin and install upstream `sooperset/mcp-atlassian` in the Hermes Docker image.
2. Replace the Rovo remote definition and legacy Basic-auth derivation with local stdio.
3. Update self-service env keys, tests and current integration documentation.
4. Run Go/security/Docker checks, rebuild the deployed image and restart the stack.
5. Accept Jira Cloud credentials through Telegram and perform a real read-only Jira check.
