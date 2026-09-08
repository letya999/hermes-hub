# Security

This is an owner-operated capable agent, not a sandbox for malicious tenants. Hermes
terminal and connector processes can access secrets inside their deployment. Prompt
injection defenses and SOUL instructions cannot remove that trust. Give connectors the
smallest account permissions suitable for your tasks and avoid sharing a bot publicly.

Never expose noVNC, OAuth callbacks, CDP or companion HTTP to the internet. Use SSH/VPN.
Never mount Docker socket or your complete home directory. Revoke leaked Telegram
sessions, OAuth grants and tokens at their providers, then rotate deployment secrets.

Report vulnerabilities privately through the repository's GitHub Security Advisories
once the owner enables private reporting. Until then contact the maintainer privately;
do not put credentials, transcripts or exploit details in a public issue. No private
reporting address or SLA is claimed before repository publication.

Supported baseline: 0.2.x. Source pins do not freeze hosted provider behavior. CI's Go/npm
checks do not scan all upstream Python/system packages in the final image; run a container scanner
and review upstream advisories before exposing new integrations.
