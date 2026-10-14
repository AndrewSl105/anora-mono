# Anora Runner API

Everything a client (the web app / Telegram Mini App) needs. The machine-readable contract is [`openapi.yaml`](openapi.yaml); this file explains behaviour.

## Conventions

- Base path `/api/v1`. JSON request and response bodies (`Content-Type: application/json`). Unknown request fields are rejected.
- **Auth**: a session cookie `anora_session` (`HttpOnly`, `Secure`, `SameSite=Lax`, 30-day sliding expiry). Every endpoint except `auth/login`, `auth/telegram` and `auth/logout` requires it; without it you get `401 unauthorized`.
- **CSRF**: every non-GET request must send `X-Requested-With: anora`, otherwise `403 csrf`.
- **Errors**: `{"error": {"code": "<machine code>", "message": "<human text>"}}`.

  | HTTP | code | when |
  |---|---|---|
  | 400 | `invalid_json`, `invalid` | malformed body, validation failure |
  | 401 | `unauthorized`, `bad_credentials` | no session; wrong login |
  | 403 | `csrf`, `forbidden` | missing header; `/metrics` from outside |
  | 404 | `not_found` | unknown id or endpoint |
  | 409 | `conflict`, `already_answered`, `in_use`, `not_running`, `no_workspace` | action not allowed in the current state |
  | 429 | `rate_limited` | 5 failed logins per IP per 15 minutes |
  | 503 | `github_disabled`, `telegram_disabled` | integration not configured |
  | 500 | `internal` | bug; details are in the server log |

- **Pagination**: list endpoints take `?limit=` (default 50, max 200) and `?cursor=`; responses are `{"items": [...], "next_cursor": "..."}`. An empty `next_cursor` means the last page. Pass it back unchanged.
- Times are RFC 3339. Ids: projects use a slug (`vatary`); everything else is an integer.

## Auth

| | |
|---|---|
| `POST /auth/login` `{password, totp}` | password **and** current 6-digit TOTP code → sets the cookie. `{"ok":true,"kind":"web"}` |
| `POST /auth/telegram` `{init_data}` | Mini App: pass `Telegram.WebApp.initData` verbatim. Valid for 1 hour after `auth_date`, only for the owner's Telegram user id → sets the cookie, `kind: "telegram"` |
| `POST /auth/logout` | deletes the session and clears the cookie |
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
| `DELETE /projects/{id}/repositories/{repoId}` | remove; `409 in_use` while tasks reference it |
| `GET /github/repositories` | repos the GitHub App can see, for the "add repo" picker: `{"items":[{full_name, default_branch, private, installation_id}]}` |

`services` accepts `postgres` and `redis`. `env` keys must match `[A-Z_][A-Z0-9_]*`; auth and service variables (`DATABASE_URL`, `REDIS_URL`, Claude tokens, …) are reserved.

## Tasks

```json
{
  "id": 12, "project_id": "vatary", "repository_id": 1,
  "title": "Add CSV export to Tables", "prompt": "…",
  "status": "running", "status_reason": null,
  "branch": "agent/12-add-csv-export-to-tables", "session_id": "…",
  "pr_number": null, "pr_url": null, "ci_status": null,
  "cost_usd_total": 0.42, "source": "web",
  "created_at": "…", "updated_at": "…", "finished_at": null, "paused_at": null
}
```

`GET /tasks/{id}` and all task actions return this object plus `repository`, `latest_run` (`{id, kind, input, exit_code, last_seq, cost_usd, started_at, finished_at}` or `null`) and `open_question` (`{id, text, options, created_at, …}` or `null`).

### Status

| status | meaning | actions available |
|---|---|---|
| `queued` | waiting for a slot | cancel |
| `preparing` | cloning, creating branch and sidecars | cancel |
| `running` | Claude is working | message (injected), interrupt, cancel |
| `waiting_input` | Claude asked a question (`open_question`) | answer, message, interrupt, cancel |
| `paused` | question not answered in time; containers removed | answer (resumes), message (resumes), cancel |
| `pushing` | validating diff, pushing, opening the PR | cancel |
| `pr_open` | PR exists; `ci_status` follows CI | message (new run on the same branch), cancel |
| `completed` | PR merged/closed, or finished with no changes | message (new run) |
| `failed` | see `status_reason` | retry |
| `cancelled` | | retry |

### Endpoints

| | |
|---|---|
| `GET /tasks?project=&status=` | newest first. `status` takes a comma-separated list |
| `POST /tasks` `{project_id, prompt, repository_id?, title?}` | `repository_id` defaults to the project's default repo; `title` to the first line of the prompt (80 chars) |
| `GET /tasks/{id}` | task view (see above) |
| `GET /tasks/{id}/events?after_id=` | timeline, oldest first; `after_id` (or `cursor`) is the last event id you have. Heartbeats are not returned |
| `GET /tasks/{id}/diff` | `{"diff", "truncated", "base", "branch"}`: `git diff base...branch` from the workspace, capped at 1 MB. `409 no_workspace` before cloning or after cleanup |
| `POST /tasks/{id}/answer` `{question_id, text}` | the first answer wins; a second gets `409 already_answered`. Answering a `paused` task resumes it |
| `POST /tasks/{id}/message` `{text}` | `running`/`waiting_input`: injected into the live session (a run handles one prompt, so text that arrives after Claude finished its turn is lost; resend it as a follow-up once the task leaves `running`). `paused`/`pr_open`/`completed`: starts a new run on the same branch with the saved session. Otherwise `409` |
| `POST /tasks/{id}/interrupt` | stop the current turn; the run then finishes ("Interrupted by the user.") with what it committed, keeping the session for a follow-up message |
| `POST /tasks/{id}/cancel` | ends the task (`cancelled`), stops the runner |
| `POST /tasks/{id}/retry` | `failed`/`cancelled` only: queues the original prompt again on the same branch |
| `GET /stats` | `{running, queued, waiting_input, cost_usd_today, cost_usd_month}` (server-local days) |

### Timeline events

`GET …/events` items:

```json
{"id": 57, "task_id": 12, "run_id": 3, "seq": 14, "type": "tool_use",
 "payload": {"type": "tool_use", "tool": "Edit", "summary": "Edit src/tables/export.ts", "ts": "…", "seq": 14},
 "created_at": "…"}
```

- **Runner events** have `seq` set and `payload` is the runner event exactly as described in [PROTOCOL.md](PROTOCOL.md): `started`, `assistant_text`, `tool_use`, `tool_result`, `question`, `usage`, `committed`, `done`, `paused`, `error`.
- **Backend events** have `seq: null`:

  | type | payload |
  |---|---|
  | `status` | `{status, reason?}` |
  | `answer` | `{question_id, text, answered_via}` (`web` or `telegram`) |
  | `message` | `{text, delivery: "injected"\|"new_run"}` |
  | `interrupt` | `{}` |
  | `run_started` | `{run_id, kind}` (`initial`, `resume`, `followup`, `retry`) |
  | `run_finished` | `{run_id, exit_code}` |
  | `pr_opened` | `{number, url}` |
  | `pr_updated` | `{number, url}` (a later run pushed to the same PR) |
  | `pr_closed` | `{number, merged}` |
  | `ci` | `{status: "pending"\|"success"\|"failure", name, url}` |
  | `diff_rejected` | `{reason}` |

## WebSocket

`GET /api/v1/ws` (session cookie required; same origin). After connecting, send one or more subscriptions:

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
- The server pings every 30 s. Messages to a slow client may be dropped. **On (re)connect, reload missed events with `GET /tasks/{id}/events?after_id=<last id>`**, then rely on pushes.

## Other

| | |
|---|---|
| `POST /webhooks/github` | GitHub only; authenticated by `X-Hub-Signature-256` |
| `GET /healthz` | `{"ok": true, "checks": {"database": "ok", "docker": "ok"}}`, `503` when a check fails |
| `GET /metrics` | Prometheus; only from the internal network (not through the reverse proxy) |
