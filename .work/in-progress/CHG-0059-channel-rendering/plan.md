# CHG-0059 Channel rendering and long replies (issue #166)

## Problem

`deliverOne` truncates every Telegram delivery to 4000 runes before `SendMessage`,
so the existing `sendDocument` long-result path is unreachable and long answers lose
their tail. `sendMessage` carries no `parse_mode`, so model Markdown (`###`, `**`,
backticks) reaches users raw. Slack gets the same unformatted text and the same
4000-rune cut, far below its real limit.

## Scope

- Parse model-authored deliveries (final response and run progress events) once with
  goldmark (+ GFM table/strikethrough/tasklist) into neutral blocks; render per
  channel: Telegram `parse_mode=HTML`, Slack mrkdwn. Model HTML is never executed:
  everything but our generated tags is escaped; links limited to http(s)/mailto.
- Split rendered parts on block boundaries within the real channel limit (Telegram
  4096 UTF-16 units; Slack 39000 runes). Oversized code fences split at line
  boundaries re-opening the fence; oversized paragraphs split at whitespace.
- One durable Delivery carries part progress (`sent_parts`, `part_count`,
  `source_sent`) persisted after each sent part; a failed part leaves the delivery
  uncertain, never a blind re-send of earlier parts. Part identity is
  `delivery_id` + index.
- Source `answer.md` is sent as a Telegram document when rendering degraded content
  (table or raw-HTML block). Slack source artifact stays in the durable spool
  record; a Slack file upload is a separate later step.
- Hub-authored replies (commands, errors, approvals, notices) stay plain text —
  their literal `<choice>`-style markers would break under HTML parsing.
- `image`/`audio` outside voice stays untouched; `#172` owns artifact handoff.

## Tests

Renderer unit tests (Cyrillic/emoji, escaping, hostile HTML, nested styles, links,
tables, code fences, boundary lengths) plus a gateway-level test for ordered
multi-part delivery, partial send failure → uncertain with persisted progress, and
Slack rendering.

## Out of scope

- Artifact (file/photo) delivery — issue #172.
- Voice readiness/TTS — issue #170.
- Live Telegram/Slack screenshots — recorded at delivery acceptance, not here.
