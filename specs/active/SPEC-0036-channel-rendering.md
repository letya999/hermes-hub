# SPEC-0036: Channel-specific rendering and long replies

Frozen: 2026-10-01. Issue 166.

1. Model-authored deliveries (final run response and run progress event text)
   carry `format: markdown` in the durable outbox record. Every other
   delivery (commands, errors, approvals, notices, voice captions) stays plain
   text and is sent byte-identical to before.

2. Markdown is parsed once into neutral blocks and rendered per channel at
   delivery time. Telegram uses `sendMessage` with `parse_mode=HTML`; the
   renderer escapes all model text and emits only hub-generated tags
   (`<b>`, `<i>`, `<s>`, `<code>`, `<pre>`, `<a>`, `<blockquote>`). Model or
   tool HTML is never executed: it is shown escaped in a `<pre>` block.
   Links are emitted only for `http(s)` and `mailto:` destinations; other
   schemes render as their link text. Slack renders the same blocks as mrkdwn
   (`*bold*`, `_italic_`, `` `code` ``, fenced blocks, `<url|text>` links,
   `>` quotes, literal `•`/`N.` list lines).

3. Tables and raw HTML blocks degrade to a monospace `<pre>`/``` ``` grid and
   flag the delivery degraded. A degraded Telegram delivery also sends the
   canonical answer once as an `answer.md` document after all text parts.

4. Replies longer than the channel limit split on rendered block boundaries:
   4000 UTF-16 code units per Telegram message (never splitting a surrogate
   pair), 39000 runes per Slack message. An oversized single block splits at
   line, then space, then rune boundaries; code/table/quote wrappers re-open
   on every part so each message is valid markup. No final answer is ever
   silently truncated.

5. One durable delivery record carries the ordered part sequence: `part_count`
   and `sent_parts` persist after each sent part, `source_sent` after the
   source document. A failed part leaves the delivery uncertain with the exact
   sent boundary — earlier parts are not resent, later parts are not lost, and
   a restarted claim resumes from `sent_parts`. Plain-text deliveries keep the
   previous single-message behavior and the 4000-rune bound.

6. `sendDocument` is an explicit Telegram API surface (`name`, `caption`,
   `data`, bounded by the media size limit); the former hidden
   "text over 4096 becomes a document" path inside `SendMessage` is removed
   because markdown parts now always fit the limit.
