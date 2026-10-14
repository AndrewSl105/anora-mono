# Anora Runner — build prompt for Claude Code

2026-10-14 ·

## 1. Context and goal

Build **Anora Runner**: a small, self-hosted Go service that runs Claude Code sessions in isolated Docker containers against my GitHub repositories and turns the result into a pull request. It is a personal tool with exactly one user (me), and it must work for any repository, not one specific project.

**How to use this document.** Put it in the new repo as `docs/SPEC.md` and tell Claude Code: "Read docs/SPEC.md and implement Anora Runner phase by phase as described in section 10. Stop after each phase and report." Everything below is written as instructions to the implementing agent.

**Who calls Anora Runner.**

- A Telegram bot that lives inside this Go service.
- A separate web frontend that also runs as a Telegram Mini App. It lives in another repo and only talks to this service's REST API and WebSocket. Anora Runner serves no HTML.

**What a task looks like from my side.** I write "Add CSV export to Tables" for project `vatary`. Anora Runner clones the repo, starts an isolated Claude Code session, streams progress to Telegram and the web, lets me answer Claude's questions, and opens a PR into `dev`. I review and merge it myself.

**Target server.** A dedicated VPS (Contabo, 4 vCPU, 8 GB RAM, 100 GB SSD, Ubuntu 24.04, Docker 29). Nothing else runs on it. It sits behind the provider's firewall with only ports 22, 80 and 443 open.

## 2. Scope, non-goals and repo layout

One repo, `anora-runner`, holds the Go backend and the TypeScript runner, because the stdin/stdout protocol ties them together and they change together.

**In scope for v1**

- Projects and repositories registry (any GitHub repo of my personal account).
- Task queue with a concurrency limit, task lifecycle, resume after pause.
- Runner container per task, with optional sidecar services (Postgres, Redis).
- GitHub App: clone, push branch, open PR.
- Real-time events over WebSocket, Telegram bot, REST API for the web client.
- Auth: password + TOTP for the web, Telegram `initData` for the Mini App.
- GitHub webhooks received and recorded (CI status), retention cleanup, metrics.

**Not in v1** (keep the design open for them, do not build them)

- Automatic CI fixing. Store CI results and notify only; the hook point must exist.
- Any web UI or HTML in this repo.
- Multiple users, roles, organisations.
- Docker access from inside the runner, Docker-in-Docker.
- Multi-repo tasks (one task = one repository).
- Running agents on a second host.

**Repo layout**

```
anora-runner/
├── cmd/anora/            # main.go: wires everything, starts HTTP + bot + scheduler
├── internal/
│   ├── config/             # env parsing and defaults
│   ├── store/              # pgx queries, migrations (embedded)
│   ├── projects/           # project + repository registry
│   ├── tasks/              # task model, state machine, service
│   ├── scheduler/          # queue, concurrency slots
│   ├── workspace/          # git clone/branch/diff/push on the host path
│   ├── githubapp/          # App auth, installation tokens, PRs, webhooks
│   ├── runnermgr/          # Docker: networks, sidecars, runner, stdio attach
│   ├── events/             # in-process pub/sub, persistence, fan-out
│   ├── api/                # REST handlers, WebSocket, middleware
│   ├── auth/               # sessions, password+TOTP, Telegram initData
│   ├── telegram/           # bot
│   └── maintenance/        # retention, reconciliation on startup
├── runner/                 # TypeScript runner + Dockerfile
├── deploy/                 # docker-compose.deploy.yml, .env.example
├── docs/                   # SPEC.md, API.md, openapi.yaml, PROTOCOL.md
└── .github/workflows/      # build images, deploy
```

## 3. Architecture and task lifecycle

The backend owns every credential and every side effect; the runner only sees a folder and a prompt. This split is the core security property: a compromised or confused Claude session cannot push code, read other repos, or touch Docker.

```
Only Anora Runner touches GitHub and Docker; the runner sees a folder

 ┌──────────────┐      ┌─────────────────────────────┐  tokens   ┌──────────────────────────┐
 │ Telegram bot │─────▶│      Anora Runner (Go)      │──────────▶│ GitHub                   │
 │ (owner only) │      │ REST, WebSocket, TG bot     │           │ clone, push, PR, webhooks│
 └──────────────┘      │ Scheduler, state machine    │           └──────────────────────────┘
 ┌──────────────┐      │ GitHub App key, Claude token│  stdin / stdout
 │Web / Mini App│─────▶│ Events: persist, fan-out    │◀─────────────────────┐
 │ (separate)   │      └──────┬───────────────┬──────┘   ┌ Network anora-task-N ┐
 └──────────────┘             ▼               ▼          │ ┌──────────────────┐ │
                        ┌──────────┐   ┌──────────────┐  │ │ Runner           │ │
                        │ Postgres │   │ docker-proxy │──┼▶│ Claude Code,     │ │
                        └──────────┘   └──────────────┘  │ │ /workspace       │ │
                                          starts         │ │ no GitHub/Docker │ │
                                                         │ └──────────────────┘ │
                                                         │ ┌──────────────────┐ │
                                                         │ │ Postgres / Redis │ │
                                                         │ └──────────────────┘ │
                                                         └──────────────────────┘
```

Clients reach only Anora Runner; Anora Runner alone holds credentials and starts containers through the filtered Docker proxy; each runner lives in its own task network with its sidecars.

**Containers**

| Container | Lifetime | Networks | Holds |
|---|---|---|---|
| `anora` (Go) | permanent | `proxy` (public via reverse proxy), `internal` | GitHub App key, Claude token, Telegram token |
| `anora-postgres` | permanent | `internal` | Anora Runner state |
| `docker-proxy` | permanent | `internal` | read-only `docker.sock`, filtered API |
| `anora-agent-<task>` | one run | `anora-task-<task>` | Claude auth only |
| `anora-svc-<task>-<name>` | one task | `anora-task-<task>` | throwaway Postgres/Redis |

**Key rules**

- Anora Runner talks to Docker only through `docker-socket-proxy` (`DOCKER_HOST=tcp://docker-proxy:2375`).
- Each task gets its own bridge network. Runner and its sidecars share it; nothing else is on it. It has internet egress (Anthropic API, npm, Go proxy, PyPI).
- Runners never join `internal` or `proxy`.
- Communication with the runner is only via its attached stdin/stdout. No ports.
- Workspaces live on the host at `WORKSPACES_DIR` (default `/srv/anora/workspaces`), bind-mounted into Anora Runner at the **same path**, so the path Anora Runner uses is also valid for Docker bind mounts.

**Task lifecycle**

1. `queued`: created via API or Telegram.
2. `preparing`: scheduler took a slot. Mint an installation token for the repo, clone into `WORKSPACES_DIR/<task_id>`, create branch `agent/<task_id>-<slug>` from the base branch, create the task network and sidecars.
3. `running`: start the runner, send `run`, stream events.
4. `waiting_input`: runner emitted `question`. Answer from API or Telegram is written to stdin; back to `running`.
5. `paused`: no answer within the question timeout, runner exited with code 3. Containers removed, workspace and Claude state volume kept. An answer or a follow-up message resumes it as a new run with `resume_session_id`.
6. `pushing`: runner finished with commits. Validate the diff (section 5), push with a fresh token, open or update the PR.
7. `pr_open`: PR exists. Webhooks update its CI status. A follow-up message starts a new run on the same branch.
8. Terminal: `completed` (PR merged or closed, or finished with no changes), `failed` (with reason), `cancelled`.

After every run, remove the runner container. The task network and sidecars live until the task is terminal or paused for longer than `SIDECAR_IDLE_TTL` (default 30 min).

## 4. Runner (`runner/`)

The runner is a Node.js process in a container that runs exactly one Claude Code session through `@anthropic-ai/claude-agent-sdk` and speaks JSON Lines over stdin/stdout. It knows nothing about GitHub, Telegram, projects or the database.

**Stack.** TypeScript (strict), Node 22+, `@anthropic-ai/claude-agent-sdk`, `zod` for protocol validation. No web framework, no network listeners.

**Auth.** Use `CLAUDE_CODE_OAUTH_TOKEN` (my subscription, created with `claude setup-token`) if set, else `ANTHROPIC_API_KEY`. Fail fast with `error{code:"no_auth"}` if neither exists. Never print either value.

**Protocol.** One JSON object per line. Document it in `docs/PROTOCOL.md` and define it once in `runner/src/protocol.ts`; mirror the types in Go (`internal/runnermgr/protocol.go`).

Commands (stdin):

| type | fields | meaning |
|---|---|---|
| `run` | `task_id`, `prompt`, `append_system_prompt?`, `resume_session_id?`, `model?`, `limits{max_turns, max_budget_usd, max_duration_sec, question_timeout_sec}`, `commit{message, author_name, author_email}` | first line, starts the session |
| `answer` | `question_id`, `text` | reply to a pending question |
| `message` | `text` | extra user message injected into the running session |
| `interrupt` | — | stop the current turn, keep the session |
| `cancel` | — | end the session, exit code 2 |

Events (stdout), every event has `type`, `ts` (RFC 3339) and `seq` (monotonic int):

| type | fields |
|---|---|
| `ready` | `runner_version` |
| `started` | `session_id`, `model` |
| `assistant_text` | `text` |
| `tool_use` | `tool`, `summary` (short, human-readable, e.g. "Edit src/tables/export.ts") |
| `tool_result` | `tool`, `ok`, `summary?` |
| `question` | `question_id`, `text`, `options?` (string array) |
| `usage` | `cost_usd_total`, `input_tokens`, `output_tokens`, `turns` |
| `committed` | `sha`, `files_changed`, `insertions`, `deletions` |
| `done` | `session_id`, `summary`, `no_changes`, `cost_usd_total` |
| `paused` | `session_id`, `reason` (`question_timeout`), `question_id` |
| `error` | `code`, `message`, `session_id?` |
| `heartbeat` | — (every 15 s) |

Exit codes: `0` done, `1` error, `2` cancelled, `3` paused, `4` limit exceeded (turns, budget or duration).

**Behaviour**

1. Read `run`. Work in `/workspace` (already a git checkout on the right branch).
2. Start the session in streaming-input mode so `message` commands can be injected mid-session. Pass `cwd`, `model`, max turns, `resume` when `resume_session_id` is set, and `append_system_prompt` on top of the default Claude Code system prompt. `CLAUDE.md` from the repo must be loaded as usual.
3. Tools: allow Read, Write, Edit, Glob, Grep, Bash, WebSearch, WebFetch, TodoWrite. Accept edits without prompting.
4. A `PreToolUse` hook denies, with a clear reason fed back to Claude: `git push`, `git remote`, `git config --global`, `git credential`, any `docker` command, writes under `.github/`, and reads of `/proc/*/environ`.
5. Questions: when Claude uses its ask-the-user tool, emit `question`, wait for `answer` up to `question_timeout_sec`, and return the answer as the tool result. On timeout emit `paused` and exit 3.
6. Limits: stop with exit 4 when turns, cost or wall time exceed the limits. Use an SDK budget option if one exists; otherwise enforce from the reported cost.
7. Map every SDK message to the events above. Keep `summary` strings short; never dump file contents into events.
8. At the end, if the tree changed: `git add -A`, commit with the given message and author, emit `committed`. Do not push. Then emit `done`.
9. Also append every event to `/workspace/.anora/events.jsonl`, and add `.anora/` to `.git/info/exclude`, so Anora Runner can replay events it missed.
10. Handle SIGTERM like `cancel`.

**Important:** SDK option and message names change between versions. Read the installed package's type definitions and current docs before coding; do not rely on memory.

**Fake mode.** With `ANORA_RUNNER_FAKE=1`, do not call Claude: emit a scripted sequence (started, a few tool events, one question, wait for the answer, write a file, commit, done). The backend's integration tests use it.

**Dockerfile**

- Base: the official Playwright image for Ubuntu 24.04 (Chromium and its system libraries included, used from phase 2 onwards).
- Add: git, Go (latest stable), Python 3 with pip and venv, build-essential, ripgrep, jq.
- Non-root user `runner` (uid 1000). Claude state in `/home/runner/.claude` (mounted per task by the backend).
- `ENTRYPOINT ["node", "/app/dist/main.js"]`.
- Tag `ghcr.io/andrewsl105/anora-agent:<git sha>` and `:latest`.

## 5. Backend (Go)

A single Go binary: REST + WebSocket API, Telegram bot, scheduler, Docker orchestration and GitHub integration. Latest stable Go, standard library first, structured logging with `log/slog` (JSON).

**Libraries** (check each for its current version and API before use)

| Need | Library |
|---|---|
| HTTP routing | `net/http` with Go 1.22+ patterns |
| Postgres | `jackc/pgx/v5` (pgxpool), plain SQL |
| Migrations | `pressly/goose/v3`, SQL files embedded with `embed` |
| Docker | `github.com/docker/docker/client` (Moby client) |
| GitHub API | `google/go-github` + `bradleyfitz/ghinstallation/v2` |
| Git | the `git` CLI via `os/exec` (installed in the image) |
| WebSocket | `coder/websocket` |
| Telegram | `mymmrac/telego` |
| Password, TOTP | `golang.org/x/crypto/bcrypt`, `pquerna/otp` |
| Metrics | `prometheus/client_golang` |

**Configuration (env)**

| Variable | Default | Notes |
|---|---|---|
| `HTTP_ADDR` | `:8080` |  |
| `PUBLIC_BASE_URL` | — | e.g. `https://anora.example.com`, used in links and PR bodies |
| `DATABASE_URL` | — |  |
| `DOCKER_HOST` | `tcp://docker-proxy:2375` |  |
| `WORKSPACES_DIR` | `/srv/anora/workspaces` | same path on host and in container |
| `RUNNER_IMAGE` | `ghcr.io/andrewsl105/anora-agent:latest` | must already be pulled locally |
| `CLAUDE_CODE_OAUTH_TOKEN` / `ANTHROPIC_API_KEY` | — | passed to runners only |
| `GITHUB_APP_ID`, `GITHUB_APP_PRIVATE_KEY_PATH`, `GITHUB_WEBHOOK_SECRET` | — |  |
| `TELEGRAM_BOT_TOKEN`, `TELEGRAM_ALLOWED_USER_ID` | — | bot ignores everyone else |
| `WEB_APP_URL` | — | URL of the separate web client / Mini App |
| `ADMIN_PASSWORD_HASH`, `ADMIN_TOTP_SECRET` | — | bcrypt hash; base32 secret |
| `SESSION_SECRET` | — | signs session cookies |
| `CORS_ORIGINS` | empty | only if web is on another origin |
| `MAX_CONCURRENT_TASKS` | `2` |  |
| `RUNNER_MEMORY` / `RUNNER_CPUS` | `3g` / `2` | per runner, hard limits |
| `SIDECAR_MEMORY` | `512m` | per sidecar |
| `DEFAULT_MODEL` | empty (SDK default) |  |
| `DEFAULT_MAX_TURNS` / `DEFAULT_MAX_BUDGET_USD` / `DEFAULT_MAX_DURATION` | `150` / `5` / `2h` |  |
| `QUESTION_TIMEOUT` | `20m` |  |
| `EVENT_RETENTION` | `720h` (30 days) |  |

Project-level settings override defaults (section 6).

**Scheduler.** Postgres is the queue. A loop claims the oldest `queued` task with `SELECT … FOR UPDATE SKIP LOCKED` when fewer than `MAX_CONCURRENT_TASKS` tasks are in `preparing`, `running` or `pushing`. Resumes of paused tasks go through the same slots. Wake the loop on new tasks via a channel and also poll every 5 s.

**Runner manager (Docker)**

- Create network `anora-task-<id>` (bridge, labelled).
- Start sidecars from the project's `services` list. Supported: `postgres` (image `postgres:17-alpine`, random password) and `redis` (`redis:7-alpine`). Wait for health. Expose them to the runner as `DATABASE_URL` and `REDIS_URL`.
- Create volume `anora-claude-<id>` mounted at `/home/runner/.claude` so sessions can resume in a new container.
- Start the runner with `OpenStdin`, `AttachStdin/Stdout/Stderr`, `StdinOnce=false`, memory and CPU limits, `PidsLimit` 1024, `no-new-privileges`, all capabilities dropped, bind mount `WORKSPACES_DIR/<id>` to `/workspace`, project env plus auth env.
- Labels on everything: `anora.task_id`, `anora.project`, `anora.run_id`, `anora.kind` (`runner` or `sidecar`). Cleanup and reconciliation select by label.
- Read stdout line by line, validate, assign to the run, persist, publish. Stderr goes to logs. On exit, read the code and map it to the state machine.

**Reconciliation on startup.** List containers with `anora.kind=runner`. Running ones: re-attach, replay `events.jsonl` from the last persisted `seq`, continue. Exited ones: replay, then apply the exit code. Tasks in `running` with no container: replay what exists, then mark `failed{reason:"lost"}`. Remove orphaned networks and sidecars.

**GitHub App and workspace**

- One private App on my personal account. Permissions: Contents RW, Pull requests RW, Metadata R, Checks R, Actions R. Events: `pull_request`, `check_suite`, `workflow_run`.
- Mint an installation token per operation, scoped to the single repository. Never write tokens to disk or `.git/config`; pass them via a credential helper or an `http.extraHeader` for that one command only.
- Clone with `--filter=blob:none`, base branch from the repository record, branch `agent/<id>-<slug>` (slug from the title, ASCII, max 40 chars). Commits are authored as `anora[bot]`.
- Before push, validate the diff against the base: reject changes under `.github/`; reject files over 5 MB; reject total diff over 20 000 changed lines; run a secrets scan (e.g. `gitleaks` binary in the Anora Runner image) and reject on findings. A rejection sets the task to `failed` with the reason and keeps the workspace for inspection.
- PR: title from the task, body with the task prompt, Claude's summary, files changed, cost, turns, and a link to the task. Create it once; later runs push to the same branch and add a PR comment with the new summary.
- Webhooks at `POST /webhooks/github`: verify `X-Hub-Signature-256`, store CI conclusions on the task, notify via Telegram, mark the task `completed` when its PR is merged or closed. Leave a clearly marked hook (`OnCIFailed`) for future auto-fixing.

**Workspace cleanup.** Delete the workspace and the Claude state volume 7 days after the task becomes terminal.

## 6. Database schema

Seven tables carry the whole state; everything else is derived. Use UUIDv7 or `bigserial` ids, `timestamptz` everywhere, and `jsonb` only where the shape is genuinely open. The schema below is the starting point; adjust names if needed but keep the relations.

```sql
CREATE TABLE projects (
  id              text PRIMARY KEY,            -- slug, e.g. 'vatary'
  name            text NOT NULL,
  instructions    text NOT NULL DEFAULT '',    -- appended to Claude's system prompt
  services        text[] NOT NULL DEFAULT '{}',-- 'postgres', 'redis'
  env             jsonb NOT NULL DEFAULT '{}', -- non-secret env for runners
  runner_image    text,                        -- NULL = RUNNER_IMAGE
  model           text,
  max_turns       int,
  max_budget_usd  numeric(8,2),
  max_duration    interval,
  runner_memory   text,
  runner_cpus     numeric(4,2),
  telegram_thread_id bigint,                   -- forum topic for this project
  archived        boolean NOT NULL DEFAULT false,
  created_at      timestamptz NOT NULL DEFAULT now(),
  updated_at      timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE repositories (
  id              bigserial PRIMARY KEY,
  project_id      text NOT NULL REFERENCES projects(id),
  full_name       text NOT NULL,               -- 'andrewsl105/vatary-server'
  base_branch     text NOT NULL DEFAULT 'dev',
  installation_id bigint NOT NULL,
  is_default      boolean NOT NULL DEFAULT false,
  UNIQUE (project_id, full_name)
);

CREATE TABLE tasks (
  id              bigserial PRIMARY KEY,       -- short numbers read well in Telegram
  project_id      text NOT NULL REFERENCES projects(id),
  repository_id   bigint NOT NULL REFERENCES repositories(id),
  title           text NOT NULL,
  prompt          text NOT NULL,
  status          text NOT NULL,               -- section 3 states
  status_reason   text,
  branch          text,
  session_id      text,                        -- latest Claude session
  pr_number       int,
  pr_url          text,
  ci_status       text,                        -- pending | success | failure
  cost_usd_total  numeric(10,4) NOT NULL DEFAULT 0,
  source          text NOT NULL,               -- 'web' | 'telegram' | 'api'
  created_at      timestamptz NOT NULL DEFAULT now(),
  updated_at      timestamptz NOT NULL DEFAULT now(),
  finished_at     timestamptz
);
CREATE INDEX ON tasks (status, created_at);

CREATE TABLE runs (                            -- one runner container lifetime
  id              bigserial PRIMARY KEY,
  task_id         bigint NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
  kind            text NOT NULL,               -- 'initial' | 'resume' | 'followup'
  input           text NOT NULL,               -- prompt, answer or follow-up text
  container_id    text,
  exit_code       int,
  last_seq        bigint NOT NULL DEFAULT 0,
  cost_usd        numeric(10,4) NOT NULL DEFAULT 0,
  started_at      timestamptz,
  finished_at     timestamptz
);

CREATE TABLE events (
  id              bigserial PRIMARY KEY,
  task_id         bigint NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
  run_id          bigint REFERENCES runs(id) ON DELETE CASCADE,
  seq             bigint,                      -- runner seq; NULL for backend events
  type            text NOT NULL,
  payload         jsonb NOT NULL,
  created_at      timestamptz NOT NULL DEFAULT now(),
  UNIQUE (run_id, seq)
);
CREATE INDEX ON events (task_id, id);

CREATE TABLE questions (
  id              text PRIMARY KEY,            -- runner question_id, prefixed with run id
  task_id         bigint NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
  run_id          bigint NOT NULL REFERENCES runs(id),
  text            text NOT NULL,
  options         jsonb,
  answer          text,
  answered_via    text,                        -- 'web' | 'telegram'
  telegram_message_id bigint,
  created_at      timestamptz NOT NULL DEFAULT now(),
  answered_at     timestamptz
);

CREATE TABLE auth_sessions (
  id              text PRIMARY KEY,            -- random 32 bytes, base64url
  kind            text NOT NULL,               -- 'web' | 'telegram'
  expires_at      timestamptz NOT NULL,
  created_at      timestamptz NOT NULL DEFAULT now(),
  last_seen_at    timestamptz NOT NULL DEFAULT now()
);
```

Backend-generated events (status changes, PR opened, CI result) go into `events` with `seq` NULL, so the web client reads one timeline per task. A first answer wins: answering an already answered question returns `409`.

## 7. REST API, WebSocket and auth

The web client is built later from `docs/API.md` and `docs/openapi.yaml` alone, so keep both complete and in sync with the code. JSON everywhere, errors as `{"error":{"code","message"}}`, cursor pagination (`?cursor=&limit=`) on lists, all routes under `/api/v1`.

**Endpoints**

| Method and path | Purpose |
|---|---|
| `POST /api/v1/auth/login` | `{password, totp}` → session cookie |
| `POST /api/v1/auth/telegram` | `{init_data}` from the Mini App → session cookie |
| `POST /api/v1/auth/logout` |  |
| `GET /api/v1/me` | session check |
| `GET /api/v1/projects` · `POST` | list, create |
| `GET /api/v1/projects/{id}` · `PATCH` · `DELETE` (archive) |  |
| `POST /api/v1/projects/{id}/repositories` · `DELETE …/{repoId}` | manage repos |
| `GET /api/v1/github/repositories` | repos the App can see, for the "add repo" picker |
| `GET /api/v1/tasks?project=&status=` · `POST` | list, create `{project_id, repository_id?, title?, prompt}` |
| `GET /api/v1/tasks/{id}` | task with latest run, open question, PR info |
| `GET /api/v1/tasks/{id}/events?after_id=` | timeline, paginated |
| `GET /api/v1/tasks/{id}/diff` | `git diff base...branch` from the workspace, capped at 1 MB |
| `POST /api/v1/tasks/{id}/answer` | `{question_id, text}` |
| `POST /api/v1/tasks/{id}/message` | follow-up or mid-session message `{text}` |
| `POST /api/v1/tasks/{id}/interrupt` · `/cancel` · `/retry` | control |
| `GET /api/v1/stats` | running and queued counts, cost today and this month |
| `GET /api/v1/ws` | WebSocket |
| `POST /webhooks/github` | GitHub (signature auth, no session) |
| `GET /healthz` · `GET /metrics` | unauthenticated, `/metrics` only from the internal network |

`message` decides by state: while `running` it is injected into the session; in `paused`, `pr_open` or `completed` it starts a new run on the same branch with the saved session; otherwise `409`.

**WebSocket.** After connect, the client sends `{"op":"subscribe","task_id":123}` or `{"op":"subscribe","scope":"tasks"}` (status changes of all tasks, for the list view). Server pushes `{"op":"event","task_id","event":{…}}` and `{"op":"task","task":{…}}`. On reconnect the client reloads missed events through `GET …/events?after_id=`. Ping every 30 s.

**Auth**

- One owner. Web login needs the password (bcrypt) **and** a TOTP code. Rate limit: 5 failed attempts per IP per 15 minutes, then `429`.
- Session = random id in an `HttpOnly`, `Secure`, `SameSite=Lax` cookie, 30-day sliding expiry, stored in `auth_sessions`.
- Mini App: verify `init_data` with the bot token per Telegram's documented HMAC algorithm, reject if `auth_date` is older than 1 hour, and require the user id to equal `TELEGRAM_ALLOWED_USER_ID`.
- State-changing requests require the `X-Requested-With: anora` header (cheap CSRF guard alongside SameSite).
- Add a CLI helper: `anora hash-password` and `anora totp-setup` (prints the secret and an `otpauth://` URI).

**Same origin.** The reverse proxy serves the web client at `/` and routes `/api`, `/webhooks`, `/healthz` to Anora Runner on the same domain, so no CORS is needed. Keep `CORS_ORIGINS` for local development.

## 8. Telegram bot

The bot runs as a goroutine in the same process, uses long polling (no public webhook needed) and calls the same task service as the REST API, never the HTTP endpoints. It answers only `TELEGRAM_ALLOWED_USER_ID`; every other update is dropped silently and logged.

**Where it talks.** Two modes, both supported:

- Private chat with the bot: commands name the project explicitly.
- A forum supergroup with one topic per project: the topic decides the project (`projects.telegram_thread_id`). `/bind <project>` inside a topic links it.

**Commands**

| Command | Action |
|---|---|
| `/new <project> <prompt>` | create a task (in a bound topic: `/new <prompt>`, or just send plain text) |
| `/tasks` | active tasks with status and inline buttons |
| `/task <id>` | details, last events, PR link |
| `/msg <id> <text>` | follow-up or mid-session message |
| `/cancel <id>` · `/retry <id>` | control |
| `/projects` | list projects |
| `/app` | button that opens the Mini App (`WEB_APP_URL`) |

**Messages**

- One status message per task, edited in place as it moves through states, with the latest one-line activity ("Editing src/tables/export.ts"). Throttle edits to one per 3 s per message and coalesce.
- Separate messages, which notify, only for: a question, PR opened, task failed, CI failed, budget or time limit hit.
- A question shows its text plus inline buttons for `options` and a "Reply with text" hint; a reply to the question message is taken as the answer. After answering, edit the message to show the answer and who answered (web or Telegram).
- Every task message has buttons: Open in app (Mini App deep link to the task), Cancel, and PR (when it exists).
- Never send diffs or long logs to Telegram; link to the app instead. Respect Telegram's 4096-character limit and escape text for the chosen parse mode.

## 9. Deployment, CI/CD and observability

Deployment follows the pattern I already use: GitHub Actions builds images, pushes them to GHCR, and the server only holds `/srv/anora/docker-compose.deploy.yml` and `.env`. No source code lives on the server.

**Anora Runner image.** Multi-stage build, final stage Debian slim or Alpine with `git`, `ca-certificates` and `gitleaks`; runs as a non-root user that can write to `WORKSPACES_DIR`. Tag `ghcr.io/andrewsl105/anora-runner:<sha>` and `:latest`.

**`deploy/docker-compose.deploy.yml`** (adapt names, keep the structure)

```yaml
services:
  anora:
    image: ghcr.io/andrewsl105/anora-runner:${ANORA_TAG:-latest}
    restart: unless-stopped
    env_file: .env
    environment:
      DOCKER_HOST: tcp://docker-proxy:2375
      WORKSPACES_DIR: /srv/anora/workspaces
    volumes:
      - /srv/anora/workspaces:/srv/anora/workspaces
      - /srv/anora/secrets/github-app.pem:/secrets/github-app.pem:ro
    networks: [proxy, internal]
    depends_on: [anora-postgres, docker-proxy]

  anora-postgres:
    image: postgres:17-alpine
    restart: unless-stopped
    environment:
      POSTGRES_USER: anora
      POSTGRES_PASSWORD: ${PG_PASSWORD}
      POSTGRES_DB: anora
    volumes: [pgdata:/var/lib/postgresql/data]
    networks: [internal]

  docker-proxy:
    image: tecnativa/docker-socket-proxy
    restart: unless-stopped
    environment:
      CONTAINERS: 1
      NETWORKS: 1
      VOLUMES: 1
      IMAGES: 1
      EXEC: 0
      POST: 1
    volumes: [/var/run/docker.sock:/var/run/docker.sock:ro]
    networks: [internal]

networks:
  proxy:    { external: true }   # shared with the reverse proxy
  internal: {}

volumes:
  pgdata:
```

No service publishes ports. The reverse proxy (Caddy or Nginx Proxy Manager on the same host, in the `proxy` network) terminates TLS. WebSocket upgrade must be enabled for `/api/v1/ws`.

**Workflows**

- `ci.yml` on pull requests: `go vet`, `go test ./...` (with a Postgres service), `golangci-lint`, runner `npm ci && npm run typecheck && npm test`.
- `release.yml` on push to `main`: build and push both images, then SSH to the server (secrets `DEPLOY_HOST`, `DEPLOY_USER`, `DEPLOY_SSH_KEY`), run `docker pull` for **both** the Anora Runner and runner images (Anora Runner starts runners from the local image and does not pull), then `docker compose -f docker-compose.deploy.yml up -d`.
- Migrations run automatically on Anora Runner startup.

**Observability**

- `slog` JSON logs with `task_id`, `run_id`, `project` on every task-related line.
- `/metrics`: tasks by status, queue length, run duration histogram, cost total per project, runner exits by code, Telegram and GitHub API errors.
- `/healthz`: DB reachable and Docker reachable.
- Back up `anora-postgres` daily with `pg_dump` (document the cron line in the README; no need to implement it in Go).

## 10. Implementation phases and working rules

Build in eight phases. Each phase ends with working, tested code, updated docs, and a short report to me; stop and wait for my go-ahead before the next phase.

1. **Runner core.** Protocol types, stdin/stdout loop, fake mode, `events.jsonl`, exit codes, unit tests for parsing and the fake sequence, Dockerfile. Done when `docker run -i` with fake mode completes a full scripted session from a JSON file piped in.
2. **Backend skeleton.** Config, logging, Postgres + migrations, projects and repositories CRUD, tasks CRUD without execution, auth (password + TOTP, sessions, CLI helpers), `docs/API.md` + `docs/openapi.yaml` started. Done when the API works end to end with `curl`.
3. **Execution with the fake runner.** Scheduler, runner manager (networks, sidecars, labels, limits, stdio attach), state machine, event persistence, answers, pause and resume, cancel, startup reconciliation. A local `git init` repo stands in for GitHub. Done when an integration test drives a task from `queued` to a local commit via the fake runner.
4. **GitHub App.** Installation tokens, clone, branch, diff validation, push, PR create and update, webhooks with signature check. Done when a task against a real test repo opens a PR.
5. **Real Claude.** Agent SDK integration in the runner: streaming input, hooks, question interception, limits, resume. Done when a real task on a small test repo opens a sensible PR and a question round-trips.
6. **Real-time and Telegram.** WebSocket hub, Telegram bot (commands, status message, questions, notifications), Mini App `initData` login.
7. **Operations.** Retention and workspace cleanup, metrics, healthz, stats endpoint, README with setup steps (GitHub App creation, TOTP setup, reverse proxy, backups).
8. **CI/CD.** `ci.yml`, `release.yml`, `deploy/` files, `.env.example` with every variable from section 5 and a comment for each.

**Working rules**

- Verify library and SDK APIs against the installed versions and their docs before writing code. Never invent option names or endpoints.
- When the spec is ambiguous or seems wrong, ask me instead of guessing. Record every decision that deviates from this spec in `docs/DECISIONS.md` with one line of reasoning.
- Keep it small: no frameworks, ORMs or abstractions beyond what a section asks for. Prefer clear code over clever code.
- Security properties in sections 3 and 5 are requirements, not suggestions: the runner gets no GitHub token, no Docker access, no host network; tokens never touch disk.
- File ownership: Anora Runner and the runner both run as uid 1000 so workspaces written by one are writable by the other; document that `/srv/anora/workspaces` must be owned by uid 1000.
- Tests: unit tests for the state machine, protocol parsing, diff validation, `initData` and webhook signature checks; integration tests with the fake runner against a real Docker daemon, skipped when Docker is unavailable.
- Small, focused commits with clear messages. Keep `docs/PROTOCOL.md`, `docs/API.md` and `docs/openapi.yaml` updated in the same commit as the code they describe: the web client will be built from them.

