# Programmatic Metadata API

tcm exposes HTTP endpoints that let agents, scripts, and CI pipelines push custom metadata to the TUI sidebar. This is the programmatic surface — your tools can self-report status, progress, and structured logs without tcm needing a dedicated watcher.

## Endpoints

All endpoints accept `POST` with `Content-Type: application/json` on `127.0.0.1:7391`.

### `POST /set-status`

Set a status pill on a session. Shows in both the session card and the detail panel.

```sh
# Set status
curl -sS -X POST http://127.0.0.1:7391/set-status \
  -H 'content-type: application/json' \
  -d '{"session":"api","text":"Indexing","tone":"info"}'

# Clear status
curl -sS -X POST http://127.0.0.1:7391/set-status \
  -H 'content-type: application/json' \
  -d '{"session":"api","text":null}'
```

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `session` | `string` | yes | Mux session name |
| `text` | `string \| null` | yes | Status text, or `null` to clear |
| `tone` | `string` | no | One of `neutral`, `info`, `success`, `warn`, `error` |

### `POST /set-progress`

Set a progress indicator on a session. Shows as a compact summary (e.g. `3/10` or `75%`).

```sh
# Set progress with current/total
curl -sS -X POST http://127.0.0.1:7391/set-progress \
  -H 'content-type: application/json' \
  -d '{"session":"api","current":3,"total":10,"label":"files"}'

# Set progress with percent
curl -sS -X POST http://127.0.0.1:7391/set-progress \
  -H 'content-type: application/json' \
  -d '{"session":"api","percent":0.75,"label":"deploying"}'

# Clear progress
curl -sS -X POST http://127.0.0.1:7391/set-progress \
  -H 'content-type: application/json' \
  -d '{"session":"api","clear":true}'
```

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `session` | `string` | yes | Mux session name |
| `current` | `number` | no | Current step |
| `total` | `number` | no | Total steps |
| `percent` | `number` | no | Progress as 0.0–1.0 (alternative to current/total) |
| `label` | `string` | no | Short label shown next to the number |
| `clear` | `boolean` | no | Set to `true` to clear progress |

### `POST /log`

Append a structured log entry to a session. Last 8 entries are visible in the detail panel.

```sh
curl -sS -X POST http://127.0.0.1:7391/log \
  -H 'content-type: application/json' \
  -d '{"session":"api","message":"Build started","source":"ci","tone":"info"}'
```

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `session` | `string` | yes | Mux session name |
| `message` | `string` | yes | Log message (max 500 chars, truncated) |
| `tone` | `string` | no | One of `neutral`, `info`, `success`, `warn`, `error` |
| `source` | `string` | no | Source label (e.g. `ci`, `build`, `agent`) |

### `POST /clear-log`

Clear all log entries for a session.

```sh
curl -sS -X POST http://127.0.0.1:7391/clear-log \
  -H 'content-type: application/json' \
  -d '{"session":"api"}'
```

### `POST /notify`

Send a notification (currently appends to logs with highlighting). Same fields as `/log`.

```sh
curl -sS -X POST http://127.0.0.1:7391/notify \
  -H 'content-type: application/json' \
  -d '{"session":"api","message":"Deploy complete","tone":"success","source":"cd"}'
```

### `POST /set-liaison-items`

Replace the items and finished conversations that the liaison block shows for a tmux session. Send the liaison's tmux session name. Items belong to the tmux session, not to a conversation: one tmux session holds one liaison, and every liaison block for that session shows the same set. A set sent for a session that does not exist is dropped at the next state build.

| Field | Type | Rule |
|-------|------|------|
| `session` | string | Required, not empty. |
| `items` | array, at most 20 | Each item has `kind`, `title`, and `since`. |
| `items[].kind` | string | One of `blocked`, `no_report`, `answer`, `reminder`. The kind `asks` is refused: the sidebar makes it from the liaison's own conversation. |
| `items[].title` | string | Folded to one line: line breaks and tabs become spaces, other control characters and format characters (such as direction overrides and zero-width characters) are removed, and the title is cut to 80 characters. A title that is empty after this is refused. |
| `items[].since` | string, RFC 3339 time | When the item started. Refused when more than one minute ahead of the server clock or more than 30 days behind it. |
| `finished` | array of strings, at most 20 | Conversation IDs, each a lower-case UUID, of conversations the liaison started that have finished and are still open. |

A request with any bad field is refused whole with `400`; on success the server answers `204`. Empty `items` and `finished` lists clear the set. The server keeps the set in memory with the time it arrived and drops it 10 minutes later, so a liaison that stops sending shows nothing stale; send the set again to keep it.

```sh
curl -sS -X POST http://127.0.0.1:7391/set-liaison-items \
  -H 'content-type: application/json' \
  -d '{"session":"home","items":[{"kind":"blocked","title":"deploy waits on a production key","since":"2026-11-16T08:50:00Z"}],"finished":["0f8fad5b-d9cb-469f-a165-70867728950e"]}'
```

`GET /state` and the websocket push carry the set in the session's `metadata`, as `liaisonItems` (with `since` in epoch milliseconds) and `liaisonFinished`. Each field is absent when empty or expired:

```json
"metadata": {
  "status": null, "progress": null, "logs": [],
  "liaisonItems": [{ "kind": "blocked", "title": "deploy waits on a production key", "since": 1794819000000 }],
  "liaisonFinished": ["0f8fad5b-d9cb-469f-a165-70867728950e"]
}
```

**These values are display only.** Any program on this machine can set them, and tcm does not check them. No program may route work, grant anything, or change a record by them.

### `GET /state`: attention fields

Each conversation in `sessions[].agents[]` (and `sessions[].agentState`) carries these optional fields; `attention` and `attentionSince` appear only when it needs the engineer:

| Field | Type | Meaning |
|-------|------|---------|
| `attention` | `"permission"`, `"question"`, `"input"`, `"error"`, or `"finished"` | Why the conversation needs the engineer. Absent when it does not. |
| `attentionSince` | number, epoch milliseconds | When that reason started. |
| `statusSince` | number, epoch milliseconds | When the conversation's current `status` started. |

- `permission`: a permission question is open.
- `question`: the question tool is open.
- `input`: Claude Code reports the conversation has waited at its prompt.
- `error`: the turn failed.
- `finished`: a turn ended and the engineer has not yet focused the conversation or typed in it.

A conversation has at most one reason; a new reason replaces the old one and restarts `attentionSince`.

```sh
curl -sS http://127.0.0.1:7391/state \
  | jq '[.sessions[].agents[] | select(.attention) | {threadName, attention, attentionSince}] | sort_by(.attentionSince)'
```

The sidebar's "needs you" block lists the same conversations, oldest first. The liaison block shows an `asks` item while the liaison's own conversation has the reason `permission`, `question`, or `error`.

### `GET /state`: liaison link fields

A liaison is a long-lived conversation that starts other conversations and gives each one a written task. The liaison's hand-out command sets three options on the tmux pane of a conversation it starts:

| Pane option | Value |
|-------------|-------|
| `@liaison-work` | The work ID of the hand-out: `w` and 1 to 6 digits, such as `w12`. |
| `@liaison-parent` | The conversation ID of the liaison. |
| `@liaison-worker` | The conversation ID of the started conversation. |

tcm reads the three options and writes none. Each conversation in `sessions[].agents[]` (and `sessions[].agentState`) carries these optional fields:

| Field | Type | Meaning |
|-------|------|---------|
| `liaisonWorkId` | string | The value of `@liaison-work`. |
| `liaisonParentThreadId` | string | The value of `@liaison-parent`. It has the form of a `threadId`. |

```json
{ "threadId": "aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee", "liaisonWorkId": "w12", "liaisonParentThreadId": "11111111-2222-4333-8444-555555555555" }
```

A field is absent unless all of these are true:

- Its option has the valid form. A work ID is `w` and 1 to 6 digits. A conversation ID is a UUID: 8-4-4-4-12 lower-case hexadecimal digits. A value of any other form, or one that holds a tab or a line break, counts as unset.
- The pane's `@liaison-worker` has the valid form and equals the conversation's own `threadId`.
- The conversation's process is alive.

A shell that stays in the pane after the conversation ends, and another conversation that starts in the same pane, get neither field.

**The fields are an unverified claim, for display only.** Any program on this machine can set the three pane options, and tcm does not check them against the liaison's record of its work. No program may use the fields or the options to route work, to grant anything, or to change a record. The liaison's own work record is the only source for which work is open.

The sidebar shows a mark before the name of each conversation whose `liaisonParentThreadId` equals the `threadId` of an open liaison, and that liaison's block shows the count of such conversations.

```sh
curl -sS http://127.0.0.1:7391/state \
  | jq '[.sessions[].agents[] | select(.liaisonParentThreadId) | {threadName, liaisonWorkId, liaisonParentThreadId}]'
```

## How It Renders

**Session card** — A compact summary line showing status text + progress:
```
  Indexing · 3/10 files
```

**Detail panel** — Full status, progress, and recent log entries with tone-colored icons:
```
ℹ Indexing
  · 3/10 files
ℹ [ci] Build started
✓ [ci] Tests passed
✗ [ci] Lint failed
```

## Tones

| Tone | Icon | Color |
|------|------|-------|
| `neutral` | `·` | gray |
| `info` | `ℹ` | blue |
| `success` | `✓` | green |
| `warn` | `⚠` | yellow |
| `error` | `✗` | red |

## Retention

- Status, progress, and liaison items are in-memory only — cleared on server restart.
- Liaison items and finished IDs are dropped 10 minutes after they arrive.
- Logs are capped at 50 entries per session (oldest are dropped).
- Metadata for deleted sessions is auto-pruned.
- Messages are truncated: status at 100 chars, logs at 500 chars.

## Example: Build Script

```bash
#!/bin/bash
SESSION=$(tmux display-message -p '#{session_name}')
URL="http://127.0.0.1:7391"

curl -sS -X POST "$URL/set-status" \
  -H 'content-type: application/json' \
  -d "{\"session\":\"$SESSION\",\"text\":\"Building\",\"tone\":\"info\"}"

curl -sS -X POST "$URL/set-progress" \
  -H 'content-type: application/json' \
  -d "{\"session\":\"$SESSION\",\"current\":0,\"total\":3,\"label\":\"steps\"}"

npm run build 2>&1 && {
  curl -sS -X POST "$URL/log" \
    -H 'content-type: application/json' \
    -d "{\"session\":\"$SESSION\",\"message\":\"Build succeeded\",\"tone\":\"success\",\"source\":\"build\"}"
} || {
  curl -sS -X POST "$URL/log" \
    -H 'content-type: application/json' \
    -d "{\"session\":\"$SESSION\",\"message\":\"Build failed\",\"tone\":\"error\",\"source\":\"build\"}"
}

# Clear when done
curl -sS -X POST "$URL/set-status" \
  -H 'content-type: application/json' \
  -d "{\"session\":\"$SESSION\",\"text\":null}"

curl -sS -X POST "$URL/set-progress" \
  -H 'content-type: application/json' \
  -d "{\"session\":\"$SESSION\",\"clear\":true}"
```
