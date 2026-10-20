# Telegram authentication plugin

Add an opt-in Go MTProto QR login module inside the Communication Hub process and Docker service. The gateway owns the short-lived browser challenge and fresh session in memory, then sends it through a signed owner-bound Telegram-only Broker endpoint. Bind the page to the loopback-published port, support Telegram two-step verification, display the account identity for confirmation, and never persist or log the session.

Verify HTTP authorization, challenge isolation, StringSession encoding and Broker transfer with fake requests, then run repository checks and local Docker checks when available.
