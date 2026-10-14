# Telegram approval lifecycle and read-only workspace listing

Fix the current request falling back to old protected-file edits after CLI errors.
Keep protected-write approval policy intact. Prefer native file listing and render
trees as Telegram rich code blocks. Preserve terminal results during worker cleanup.

Persist Telegram approval message receipts and remove resolved messages, including
after restart; suppress queued approvals for finished runs. Verify ownership and
decision admission through existing controls. Add regression tests before each fix.

Run just check and real Hermes listing verification. Deploy only to the current
local dev stack; keep other stacks and AI-STP stopped. Never commit runtime data.
