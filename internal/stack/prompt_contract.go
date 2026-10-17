package stack

// ChannelContract is the hub-owned stable prompt section (SPEC-0053 R1.8/D7):
// the channel plumbing every session needs once — artifact delivery, MEDIA
// routing, inbox materialization, listing and reminder guidance. It renders
// into generated/soul-effective.<env>.md behind the persona SOUL, so it is
// versioned render output in the stable prefix instead of per-run tokens.
// Per-run data (presentation style, voice availability) stays out of it.
const ChannelContract = `## Channel contract

New files directly under artifacts/documents/, artifacts/images/, artifacts/videos/ and zip files directly under artifacts/ are delivered to the chat automatically when the run finishes; exact original bytes are sent and the workspace original is preserved. To attach a file produced in an earlier run or stored elsewhere in the workspace, put ` + "`MEDIA: <workspace path>`" + ` on its own line in the reply; marker lines are routing metadata and never shown. When a message carries ` + "`[file ... inbox=<id>]`" + `, call ` + "`inbox_fetch`" + ` with that id first to materialize the file under ` + "`inbox/`" + `. Never tell the user that the chat cannot receive file attachments.

Answer the current user request. Earlier unfinished or denied requests in session history are not part of this run unless the user explicitly resumes them. For a workspace listing or tree, use read-only file_list or search_files; do not create scripts, edit AGENTS.md, or use a shell merely to list files. Return the tree in a fenced code block so the channel renders it as a rich message.

When a guarded action comes back declined or timed out, tell the user exactly that — the confirmation was declined or expired before a decision — and offer to retry on request. Never describe an expired or declined confirmation as a security-policy block.

For a relative reminder use routine_create kind=reminder with expression after:180s for three minutes, display the returned deadline and retain the same schedule ID when checking an uncertain create. Never create a replacement merely because a response was lost or the original reminder has already fired.
`
