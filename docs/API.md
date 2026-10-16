# Anora Runner API

Everything a client (the web app / Telegram Mini App) needs. The machine-readable contract is [`openapi.yaml`](openapi.yaml); this file explains behaviour.

## Conventions

- Base path `/api/v1`. JSON request and response bodies (`Content-Type: application/json`). Unknown request fields are rejected. `POST /tasks` and `POST /tasks/{id}/message` also accept `multipart/form-data` to attach images (see [Attachments](#attachments)).
- **Auth**: a session cookie `anora_session` (`HttpOnly`, `Secure`, `SameSite=Lax`, 30-day sliding expiry). Every endpoint except `auth/login`, `auth/telegram` and `auth/logout` requires it; without it you get `401 unauthorized`. **Telegram sessions** may instead send `Authorization: Bearer <token>` (the `token` from `auth/telegram`), because Telegram Web embeds the Mini App in a cross-site iframe where a `SameSite=Lax` cookie is never sent. Browser (`web`) sessions are cookie-only.
- **CSRF**: every non-GET request must send `X-Requested-With: anora`, otherwise `403 csrf`.
- **Errors**: `{"error": {"code": "<machine code>", "message": "<human text>"}}`.

  | HTTP | code | when |
  |---|---|---|
  | 400 | `invalid_json`, `invalid` | malformed body, validation failure |
  | 401 | `unauthorized`, `bad_credentials` | no session; wrong login |
  | 403 | `csrf`, `forbidden` | missing header; `/metrics` from outside |
  | 404 | `not_found` | unknown id or endpoint |
  | 409 | `conflict`, `already_answered`, `in_use`, `not_running`, `no_workspace` | action not allowed in the current state |
  | 410 | `gone` | the attachment was removed |
  | 429 | `rate_limited` | 5 failed logins per IP per 15 minutes |
  | 503 | `github_disabled`, `telegram_disabled` | integration not configured |
  | 500 | `internal` | bug; details are in the server log |

- **Pagination**: list endpoints take `?limit=` (default 50, max 200) and `?cursor=`; responses are `{"items": [...], "next_cursor": "..."}`. An empty `next_cursor` means the last page. Pass it back unchanged (for `GET /tasks/{id}/events?before_id=`, pass it back as `before_id`).
- Times are RFC 3339. Ids: projects use a slug (`vatary`); everything else is an integer.

## Auth

| | |
|---|---|
| `POST /auth/login` `{password, totp}` | password **and** current 6-digit TOTP code → sets the cookie. `{"ok":true,"kind":"web"}` |
| `POST /auth/telegram` `{init_data}` | Mini App: pass `Telegram.WebApp.initData` verbatim. Valid for 1 hour after `auth_date`, only for the owner's Telegram user id → sets the cookie and returns `{"ok":true,"kind":"telegram","token":"…"}`. Keep `token` in memory only (never `localStorage`) and send it as `Authorization: Bearer`; it is the same session as the cookie |
| `POST /auth/logout` | deletes the session (cookie or bearer token) and clears the cookie |
| `GET /me` | `{"ok":true,"kind":"web"\|"telegram"}`; use it as the session check on app start |

## Projects and repositories

A **project** groups repositories and holds run settings. All override fields are nullable: `null` means "use the server default". In `PATCH`, an omitted field is unchanged; `""` or `0` resets an override to the default.

```json
{
  "id": "vatary", "name": "Vatary",
  "instructions": "appended to Claude's system prompt",
  "services": ["postgres", "redis"],
  "env": {"NODE_ENV": "test"},
  "runner_image": null, "model": null, "max_turns": null, "max_budget_usd": null,
  "max_duration_sec": null, "runner_memory": null, "runner_cpus": null,
  "telegram_thread_id": null, "archived": false,
  "created_at": "…", "updated_at": "…",
  "repositories": [{"id": 1, "project_id": "vatary", "full_name": "andrewsl105/vatary-server",
                    "base_branch": "dev", "installation_id": 123, "is_default": true}]
}
```

| | |
|---|---|
| `GET /projects?archived=true` | list (archived hidden unless asked), ordered by id |
| `POST /projects` | create; body = project fields plus `id` (lowercase slug, max 40). `409` if the id exists |
| `GET /projects/{id}` | one project with repositories |
| `PATCH /projects/{id}` | update any writable field |
| `DELETE /projects/{id}` | archive (sets `archived: true`; nothing is deleted) |
| `POST /projects/{id}/repositories` `{full_name, installation_id, base_branch?, is_default?}` | add a repo. `base_branch` defaults to `dev`. The first repo, or one with `is_default`, becomes the default |
| `PATCH /projects/{id}/repositories/{repoId}` `{base_branch?, is_default?}` | omitted fields are unchanged; returns the repository. `is_default: true` makes it the project's only default; `is_default: false` on the current default is `409 conflict` (make another repo the default instead). Changing `base_branch` is `409 in_use` while tasks that are not `completed`/`failed`/`cancelled` use the repo |
| `DELETE /projects/{id}/repositories/{repoId}` | remove; `409 in_use` while tasks reference it |
| `GET /github/repositories` | repos the GitHub App can see, for the "add repo" picker: `{"items":[{full_name, default_branch, private, installation_id}]}` |

`base_branch` must be a plain git branch name (letters, digits, `._/-`, not starting with `-` or `.`, no `..`); otherwise `400 invalid`.

`services` accepts `postgres` and `redis`. `env` keys must match `[A-Z_][A-Z0-9_]*`; auth and service variables (`DATABASE_URL`, `REDIS_URL`, Claude tokens, …) are reserved.

## Tasks

```json
{
  "id": 12, "project_id": "vatary", "repository_id": 1,
  "title": "Add CSV export to Tables", "prompt": "…",
  "status": "running", "status_reason": null,
  "branch": "agent/12-add-csv-export-to-tables", "session_id": "…",
  "pr_number": null, "pr_url": null, "ci_status": null,
  "cost_usd_total": 0.42, "turns": 17, "source": "web",
  "created_at": "…", "updated_at": "…", "finished_at": null, "paused_at": null,
  "last_activity": "Edit src/tables/export.ts", "last_activity_at": "…",
  "queued_message": null
}
```

- `turns`: Claude turns across all runs of the task (sum of each run's last reported `usage.turns`).
- `last_activity`: one line (max 120 characters) describing the latest thing that happened: the latest `tool_use` summary, or the latest status change as `<status>` / `<status>: <reason>`, whichever came last. `last_activity_at` is when it was set. Both are `null` only for tasks that predate the field and have no surviving events.
- `queued_message`: text sent with `POST …/message` while a run was active (several messages are joined by a blank line), or `null`. It becomes the input of the next run as soon as the current run ends.

`GET /tasks/{id}` and all task actions return this object plus `repository`, `latest_run` (`{id, kind, input, exit_code, last_seq, cost_usd, turns, started_at, finished_at}` or `null`) and `open_question` (`{id, text, options, created_at, …}` or `null`).

### Status

| status | meaning | actions available |
|---|---|---|
| `queued` | waiting for a slot | message (queued), cancel |
| `preparing` | cloning, creating branch and sidecars | message (queued), cancel |
| `running` | Claude is working | message (queued), interrupt, cancel |
| `waiting_input` | Claude asked a question (`open_question`) | answer, message (queued), interrupt, cancel |
| `paused` | question not answered in time; containers removed | answer (resumes), message (resumes), cancel |
| `pushing` | validating diff, pushing, opening the PR | message (queued), cancel |
| `pr_open` | PR exists; `ci_status` follows CI | message (new run on the same branch), cancel |
| `completed` | PR merged/closed, or finished with no changes | message (new run) |
| `failed` | see `status_reason` | retry |
| `cancelled` | | retry |

### Endpoints

| | |
|---|---|
| `GET /tasks?project=&status=` | newest first. `status` takes a comma-separated list |
| `POST /tasks` `{project_id, prompt, repository_id?, title?}` | `repository_id` defaults to the project's default repo; `title` to the first line of the prompt (80 chars). As multipart, add images in `files[]` |
| `GET /tasks/{id}` | task view (see above) |
| `GET /tasks/{id}/events?after_id=` | timeline, oldest first; `after_id` (or `cursor`) is the last event id you have. Heartbeats are not returned |
| `GET /tasks/{id}/events?before_id=` | timeline backwards, **newest first**: events older than `before_id`; `before_id=0` starts at the newest event. Pass `next_cursor` back as `before_id` (not as `cursor`, which means `after_id`). Combining `before_id` with `after_id`/`cursor` is `400 invalid` |
| `GET /tasks/{id}/runs` | the task's runs, oldest first, including a queued one (`started_at: null`): `{id, task_id, kind, input, container_id, exit_code, last_seq, cost_usd, turns, started_at, finished_at}`. `kind` is `initial`, `resume`, `followup` or `retry`; `cost_usd` is the run's own cost. Paginated with `limit`/`cursor` |
| `GET /tasks/{id}/diff` | `{"diff", "truncated", "base", "branch"}`: `git diff base...branch` from the workspace, capped at 1 MB. `409 no_workspace` before cloning or after cleanup |
| `POST /tasks/{id}/answer` `{question_id, text}` | the first answer wins; a second gets `409 already_answered`. Answering a `paused` task resumes it |
| `POST /tasks/{id}/message` `{text}` (multipart: plus `files[]`) | `queued`/`preparing`/`running`/`waiting_input`/`pushing`: **queued** (a run handles one prompt; nothing is injected into a live session). The text is kept in `queued_message` and starts the next run (same branch, same session) as soon as the current run ends: on `pr_open`/`completed` as a follow-up, on `paused` as a resume. If the task fails or is cancelled first, the text is dropped and a `message_dropped` event shows it. `paused`/`pr_open`/`completed`: starts a new run right away. Otherwise `409` |
| `POST /tasks/{id}/interrupt` | `running`/`waiting_input`: stops Claude and **ends the run** ("Interrupted by the user."); what it committed is pushed as for any finished run, and the session is kept. A queued message then starts the next run; otherwise send a follow-up message |
| `POST /tasks/{id}/cancel` | ends the task (`cancelled`), stops the runner |
| `POST /tasks/{id}/retry` | `failed`/`cancelled` only: queues the original prompt again on the same branch |
| `GET /tasks/{id}/attachments/{attachmentId}` | the image (`?thumb=1`: JPEG, longest side ≤ 480 px); `410 gone` once removed |
| `GET /stats` | `{running, queued, waiting_input, cost_usd_today, cost_usd_month}` (server-local days) |

### Timeline events

`GET …/events` items:

```json
{"id": 57, "task_id": 12, "run_id": 3, "seq": 14, "type": "tool_use",
 "payload": {"type": "tool_use", "tool": "Edit", "summary": "Edit src/tables/export.ts", "ts": "…", "seq": 14},
 "created_at": "…"}
```

Every event type has a typed schema in `openapi.yaml`: `Event` is a `oneOf` discriminated by the envelope's `type` (`ToolUseEvent`, `StatusEvent`, …), each with its own `payload` schema (`ToolUsePayload`, `StatusPayload`, …). Generate client types from it; `switch (event.type)` narrows the payload.

- **Runner events** have `seq` (and `run_id`) set and `payload` is the runner event exactly as described in [PROTOCOL.md](PROTOCOL.md), including its own `type`, `ts` and `seq`: `ready`, `started`, `assistant_text`, `tool_use`, `tool_result`, `question`, `usage`, `committed`, `done`, `paused`, `error`. In a `question` payload, `question_id` is the runner-local id (`q1`); the API question id (for `/answer`) is `<run_id>-<question_id>`, as in `open_question.id`.
- **Backend events** have `seq: null`; their payload has no `type`, `ts` or `seq` (use the envelope's `type` and `created_at`):

  | type | payload |
  |---|---|
  | `created` | `{attachments}`: the task was created, with the images attached to it (`[]` when none). Tasks created before this event existed have none |
  | `status` | `{status, reason?}` |
  | `answer` | `{question_id, text, answered_via}` (`web` or `telegram`) |
  | `message` | `{text, delivery: "queued"\|"new_run", attachments?}` (`"injected"` only in timelines recorded before messages were queued; `attachments` only when images were sent) |
  | `message_dropped` | `{text, reason: "failed"\|"cancelled"}`: a queued message that never started a run |
  | `interrupt` | `{}` |
  | `run_started` | `{run_id, kind}` (`initial`, `resume`, `followup`, `retry`) |
  | `run_finished` | `{run_id, exit_code}` |
  | `pr_opened` | `{number, url}` |
  | `pr_updated` | `{number, url}` (a later run pushed to the same PR) |
  | `pr_closed` | `{number, merged}` |
  | `ci` | `{status: "pending"\|"success"\|"failure", name, url}` |
  | `diff_rejected` | `{reason}` |

### Attachments

Images go with a task's prompt or with a message. Send the request as `multipart/form-data`: the same fields as the JSON body (`project_id`, `repository_id`, `title`, `prompt`, or `text`) plus up to 10 files in `files[]`. The text is still required.

- Accepted: PNG, JPEG, WebP and GIF, detected from the content (name and declared type are ignored). At most 40 MB per file, 100 MB per request and 50 megapixels per image. Animated GIF and WebP are rejected. Anything else is `400 invalid` with a message naming the file.
- Metadata is removed before storing: JPEG, PNG and GIF are re-encoded (a JPEG's EXIF orientation is applied to the pixels first); WebP loses its EXIF and XMP chunks. `size`, `width` and `height` describe the stored file.
- Each attachment is `{id, name, mime, size, width, height}`. Task attachments are listed in the task's `created` event, message attachments in its `message` event.
- Download with `GET /tasks/{id}/attachments/{attachmentId}` (session required like every endpoint; `?thumb=1` for a thumbnail). Telegram Web clients send the bearer token, so fetch the image and show it from a blob URL rather than an `<img src>`.
- The agent sees the images of the input it is working on (the prompt, or the messages a run starts with) at `/attachments/<id>.<ext>`, plus the task's earlier images. They are removed (`410 gone`, files deleted) when the task becomes `completed`, and with the workspace for `failed` and `cancelled` tasks (`WORKSPACE_RETENTION`), so Retry and follow-ups still have them until then. Images of a queued message that is dropped (`message_dropped`) are removed with it.

## WebSocket

`GET /api/v1/ws` (session cookie required; same origin). Without a cookie (Telegram Web), first `POST /api/v1/ws/ticket` with the bearer token → `{"ticket":"…","expires_in":30}`, then connect to `/api/v1/ws?ticket=…`. A ticket works once and only within `expires_in` seconds, so fetch a new one for every (re)connect. After connecting, send one or more subscriptions:

```json
{"op": "subscribe", "task_id": 123}
{"op": "subscribe", "scope": "tasks"}
{"op": "unsubscribe", "task_id": 123}
```

The server pushes:

```json
{"op": "event", "task_id": 123, "event": { /* timeline event as above */ }}
{"op": "task", "task": { /* task object */ }}
{"op": "error", "message": "…"}
```

- `task_id` subscriptions receive that task's `event` and `task` messages.
- `scope: "tasks"` receives `task` messages for every task (for the list view), no events.
- A `task` message is sent on every status change and also whenever `turns`, `last_activity`, `last_activity_at` or `queued_message` change (each `tool_use` and `usage` event, each queued message), so the list view stays live without event subscriptions.
- The server pings every 30 s. Messages to a slow client may be dropped. **On (re)connect, reload missed events with `GET /tasks/{id}/events?after_id=<last id>`**, then rely on pushes.

## Other

| | |
|---|---|
| `POST /webhooks/github` | GitHub only; authenticated by `X-Hub-Signature-256` |
| `GET /healthz` | `{"ok": true, "checks": {"database": "ok", "docker": "ok"}}`, `503` when a check fails |
| `GET /metrics` | Prometheus; only from the internal network (not through the reverse proxy) |
