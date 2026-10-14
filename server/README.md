# Anora Runner

A self-hosted, single-owner Go service that runs Claude Code sessions in isolated Docker containers against your GitHub repositories and turns the result into a pull request. You drive it from a Telegram bot or a separate web app / Telegram Mini App (REST + WebSocket).

```
Telegram bot ─┐                         ┌─ GitHub (clone, push, PR, webhooks)
Web/Mini App ─┼─► Anora Runner (Go) ────┤
              │   API · bot · scheduler └─ docker-proxy ─► per task: network anora-task-N
              │   holds every credential                    ├─ runner (Claude Code, /workspace)
              └── Postgres                                  └─ postgres / redis sidecars
```

The backend owns every credential and side effect. A runner sees only its workspace folder and a prompt: it gets no GitHub token, no Docker access and no host network.

- Specification: [docs/SPEC.md](docs/SPEC.md)
- Client API: [docs/API.md](docs/API.md), [docs/openapi.yaml](docs/openapi.yaml)
- Runner protocol: [docs/PROTOCOL.md](docs/PROTOCOL.md)
- Deviations from the spec: [docs/DECISIONS.md](docs/DECISIONS.md)

## Repository layout

```
cmd/anora/            main: serve, migrate, hash-password, totp-setup
internal/config       env parsing and defaults
internal/store        SQL (pgx) and embedded goose migrations
internal/projects     project + repository registry
internal/tasks        task model, state machine, task service (shared by API and bot)
internal/scheduler    queue, concurrency slots, run supervision, startup reconciliation
internal/runnermgr    Docker: networks, sidecars, volumes, runner containers, stdio protocol
internal/workspace    git clone/branch/diff/push, diff validation, secrets scan
internal/githubapp    App auth, installation tokens, PRs, webhooks
internal/events       in-process pub/sub
internal/api          REST, WebSocket, middleware
internal/auth         sessions, password + TOTP, Telegram initData
internal/telegram     the bot
internal/maintenance  retention, workspace cleanup, idle sidecars
internal/metrics      Prometheus
runner/               TypeScript runner (Claude Agent SDK) + Dockerfile
deploy/               docker-compose.deploy.yml, .env.example
```

## Server setup

Target: a dedicated Ubuntu 24.04 host with Docker, ports 22/80/443 open, a reverse proxy (Caddy or Nginx Proxy Manager) on an external Docker network called `proxy`.

### 1. Directories

```sh
sudo mkdir -p /srv/anora/workspaces /srv/anora/secrets /srv/anora/backups
# Anora Runner and the runner containers both run as uid 1000.
sudo chown -R 1000:1000 /srv/anora/workspaces
docker network create proxy   # if your reverse proxy doesn't already own it
```

Copy `deploy/docker-compose.deploy.yml` to `/srv/anora/` and `deploy/.env.example` to `/srv/anora/.env`, then fill it in as described below. No source code goes on the server.

### 2. Owner login (password + TOTP)

```sh
docker run --rm -it ghcr.io/andrewsl105/anora-runner:latest hash-password   # prints ADMIN_PASSWORD_HASH
docker run --rm ghcr.io/andrewsl105/anora-runner:latest totp-setup          # prints ADMIN_TOTP_SECRET and an otpauth:// URI
```

Add the `otpauth://` URI to your authenticator app (paste it, or render it as a QR code, e.g. `qrencode -t ansiutf8 '<uri>'`). Put both values in `.env`; quote the bcrypt hash with single quotes, because it contains `$`. Also set `SESSION_SECRET` (`openssl rand -hex 32`).

### 3. Claude auth

Run `claude setup-token` on your machine and put the result in `CLAUDE_CODE_OAUTH_TOKEN`. Alternatively set `ANTHROPIC_API_KEY`. Only runner containers receive it.

### 4. GitHub App

On GitHub: **Settings → Developer settings → GitHub Apps → New GitHub App** (on your personal account).

- **Homepage URL**: your `PUBLIC_BASE_URL`.
- **Webhook URL**: `https://<your domain>/webhooks/github`; **Webhook secret**: `openssl rand -hex 32` (put it in `GITHUB_WEBHOOK_SECRET`).
- **Repository permissions**: Contents *read & write*, Pull requests *read & write*, Metadata *read*, Checks *read*, Actions *read*.
- **Subscribe to events**: Pull request, Check suite, Workflow run.
- **Where can this App be installed**: Only on this account.

After creating it, note the **App ID** (`GITHUB_APP_ID`), generate a **private key**, and save it as `/srv/anora/secrets/github-app.pem` (`chmod 600`, owner uid 1000). Then **Install App** on the repositories Anora Runner may work on. In the web app, the "add repository" picker (`GET /api/v1/github/repositories`) lists them together with their installation id.

### 5. Telegram

Create a bot with [@BotFather](https://t.me/BotFather) (`TELEGRAM_BOT_TOKEN`) and find your numeric user id, for example with [@userinfobot](https://t.me/userinfobot) (`TELEGRAM_ALLOWED_USER_ID`). The bot ignores everyone else. For a Mini App, register `WEB_APP_URL` with BotFather (`/newapp`) and optionally set `TELEGRAM_MINIAPP_LINK=https://t.me/<bot>/<app>` for deep links in groups.

For one topic per project: create a forum supergroup, add the bot as an admin, open a topic and send `/bind <project>`.

### 6. Reverse proxy

Serve the web client at `/` and route `/api`, `/webhooks` and `/healthz` to `anora:8080` on the same domain, with WebSocket upgrades enabled for `/api/v1/ws`. Do not expose `/metrics`; it only answers requests that come directly from the internal network. Caddy example:

```
anora.example.com {
	@backend path /api/* /webhooks/* /healthz
	reverse_proxy @backend anora:8080
	reverse_proxy web:80
}
```

(Caddy proxies WebSocket upgrades automatically. In Nginx Proxy Manager, enable "Websockets Support".)

### 7. Start

```sh
cd /srv/anora
docker pull ghcr.io/andrewsl105/anora-agent:latest   # runners are never pulled automatically
docker compose -f docker-compose.deploy.yml up -d
curl -s https://anora.example.com/healthz
```

Migrations run on startup. After that, the release workflow deploys every push to `main` (see below).

### 8. Backups

Back up Postgres daily with `pg_dump`. Example root crontab line (keeps 14 days):

```
15 3 * * * docker exec anora-postgres pg_dump -U anora -Fc anora > /srv/anora/backups/anora-$(date +\%F).dump && find /srv/anora/backups -name 'anora-*.dump' -mtime +14 -delete
```

Restore with `docker exec -i anora-postgres pg_restore -U anora -d anora --clean < backup.dump`. Workspaces are disposable (the branch lives on GitHub); the database is the state.

## CI/CD

- **`ci.yml`** (pull requests): `go vet`, `go test -p 1 ./...` against a Postgres service, with the fake runner image built for the Docker integration tests; `golangci-lint`; and the runner's `npm ci && npm run typecheck && npm test`.
- **`release.yml`** (push to `main`): builds and pushes `ghcr.io/andrewsl105/anora-runner` and `ghcr.io/andrewsl105/anora-agent` (tags `<sha>` and `latest`, linux/amd64). Then it connects over SSH, pulls **both** images (Anora Runner never pulls the runner image itself) and runs `docker compose -f docker-compose.deploy.yml up -d`.

Repository secrets for the deploy job (environment `production`): `DEPLOY_HOST`, `DEPLOY_USER`, `DEPLOY_SSH_KEY`. If the GHCR packages are private, log the server in once with a token that has `read:packages`: `echo <token> | docker login ghcr.io -u andrewsl105 --password-stdin`.

## Operations

- **Health**: `GET /healthz` checks Postgres and Docker.
- **Metrics** (`GET /metrics`, internal only): `anora_tasks{status}`, `anora_queue_length`, `anora_run_duration_seconds{project}`, `anora_cost_usd_total{project}`, `anora_runner_exits_total{code}`, `anora_telegram_api_errors_total{op}`, `anora_github_api_errors_total{op}`.
- **Logs**: JSON (`log/slog`) on stderr; task lines carry `task_id`, `run_id` and `project`.
- **Retention** (every 10 minutes): runner events of finished tasks older than `EVENT_RETENTION` are deleted (status events stay); workspaces and Claude state volumes are deleted 7 days after a task finishes; sidecars of tasks idle in `paused`/`pr_open` for `SIDECAR_IDLE_TTL` are removed; expired sessions are purged.
- **Restart safety**: on startup, running runners are re-attached and missed events are replayed from `<workspace>/.anora/events.jsonl`. Exits that happened while the service was down are applied, runs whose container vanished are failed with reason `lost`, interrupted preparation is re-queued, interrupted pushes are retried, and orphaned containers and networks are removed.

## Development

Requirements: Go 1.27+, Node 22+, Docker, git, and gitleaks (`brew install gitleaks`).

```sh
# Runner
cd runner && npm ci && npm run typecheck && npm test && cd ..
docker build -t anora-agent:dev runner/

# Backend tests: a throwaway Postgres, then all packages one at a time.
# The Docker integration tests use the fake runner image built above. They
# must not run in parallel, because each scheduler removes containers its
# database doesn't know about.
docker run -d --name anora-dev-pg -e POSTGRES_USER=anora -e POSTGRES_PASSWORD=anora -p 55432:5432 postgres:17-alpine
export ANORA_TEST_DATABASE_URL=postgres://anora:anora@localhost:55432/anora?sslmode=disable
go test -p 1 ./...
```

To run the service locally, use a `.env` based on `deploy/.env.example` with `DOCKER_HOST=unix:///var/run/docker.sock`, `COOKIE_INSECURE=true`, `RUNNER_IMAGE=anora-agent:dev` and, without Claude credentials, `ANORA_RUNNER_FAKE=1`. Without a GitHub App the service still runs tasks, and commits stay in the local workspace.

Then `go run ./cmd/anora serve`.
