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
deploy/               docker-compose.deploy.yml, Caddyfile, .env.example; docker-compose.dev.yml (local)
```

## Server setup

One server, one domain: Anora Runner, its Postgres, the Docker socket proxy, the web client ([anora-client](https://github.com/andrewsl105/anora-client)) and Caddy (TLS, routing) all run from `/srv/anora/docker-compose.deploy.yml`. Caddy serves `https://<domain>`: `/api/*` (including the WebSocket `/api/v1/ws`), `/webhooks/*` and `/healthz` go to `anora:8080`, everything else to `anora-client:80`; `/metrics` answers 404 from outside.

Target: a fresh Ubuntu 24.04 VPS with Docker Engine and the Compose plugin, nothing else. Open ports 22, 80 and 443 (and 443/udp for HTTP/3). Point the domain's DNS A/AAAA record at the server before the first start, so Caddy can get the certificate. Below, `anora.example.com` stands for your domain.

### 1. Deploy user and directories

The release workflows log in over SSH as a deploy user that can run Docker. On Ubuntu cloud images the default user (`ubuntu`) already has uid 1000, which is also the uid of Anora Runner and the runner containers; any user in the `docker` group works.

```sh
sudo usermod -aG docker "$USER"            # the deploy user; log in again afterwards
sudo mkdir -p /srv/anora/workspaces /srv/anora/secrets /srv/anora/backups
sudo chown "$USER": /srv/anora              # the workflows write /srv/anora/.deploy.lock
# Anora Runner and the runner containers run as uid 1000 and share the workspaces.
sudo chown -R 1000:1000 /srv/anora/workspaces /srv/anora/secrets
```

Copy three files from this repository to the server; no source code goes there:

```sh
scp deploy/docker-compose.deploy.yml deploy/Caddyfile <user>@<server>:/srv/anora/
scp deploy/.env.example <user>@<server>:/srv/anora/.env
```

The `proxy` network (Caddy, `anora`, `anora-client`) is created by the compose file (`anora_proxy`); there is no `docker network create` step and no other reverse proxy.

### 2. `.env`

Fill in `/srv/anora/.env` (every variable is described in `deploy/.env.example`). The ones that must be set:

| Variable | Value |
|---|---|
| `ANORA_DOMAIN` | `anora.example.com` (Caddy's site address) |
| `PUBLIC_BASE_URL` | `https://anora.example.com` |
| `WEB_APP_URL` | `https://anora.example.com` (the client is on the same domain) |
| `PG_PASSWORD` | `openssl rand -hex 24` |
| `SESSION_SECRET` | `openssl rand -hex 32` |
| `ADMIN_PASSWORD_HASH`, `ADMIN_TOTP_SECRET` | step 3 |
| `CLAUDE_CODE_OAUTH_TOKEN` (or `ANTHROPIC_API_KEY`) | step 4 |
| `GITHUB_APP_ID`, `GITHUB_WEBHOOK_SECRET` | step 5 |
| `TELEGRAM_BOT_TOKEN`, `TELEGRAM_ALLOWED_USER_ID`, `TELEGRAM_MINIAPP_LINK` | step 6 |

Leave `COOKIE_INSECURE=false`, `CORS_ORIGINS` empty (same origin), `ANORA_RUNNER_FAKE=false`, and `WORKSPACES_DIR=/srv/anora/workspaces`. `chmod 600 /srv/anora/.env`.

### 3. Owner login (password + TOTP)

These commands need the image: run the anora-runner release workflow once first (and, for private packages, `docker login ghcr.io` as in step 7).

```sh
docker run --rm -it ghcr.io/andrewsl105/anora-runner:latest hash-password   # prints ADMIN_PASSWORD_HASH
docker run --rm ghcr.io/andrewsl105/anora-runner:latest totp-setup          # prints ADMIN_TOTP_SECRET and an otpauth:// URI
```

Add the `otpauth://` URI to your authenticator app (paste it, or render it as a QR code, e.g. `qrencode -t ansiutf8 '<uri>'`). Quote the bcrypt hash with single quotes in `.env`, because it contains `$`.

### 4. Claude auth

Run `claude setup-token` on your machine and put the result in `CLAUDE_CODE_OAUTH_TOKEN`. Alternatively set `ANTHROPIC_API_KEY`. Only runner containers receive it.

### 5. GitHub App

On GitHub: **Settings → Developer settings → GitHub Apps → New GitHub App** (on your personal account).

- **Homepage URL**: `https://anora.example.com`.
- **Webhook URL**: `https://anora.example.com/webhooks/github`; **Webhook secret**: `openssl rand -hex 32` (put it in `GITHUB_WEBHOOK_SECRET`).
- **Repository permissions**: Contents *read & write*, Pull requests *read & write*, Metadata *read*, Checks *read*, Actions *read*.
- **Subscribe to events**: Pull request, Check suite, Workflow run.
- **Where can this App be installed**: Only on this account.

After creating it, note the **App ID** (`GITHUB_APP_ID`), generate a **private key**, and save it as `/srv/anora/secrets/github-app.pem` (`sudo chown 1000:1000` and `chmod 600`). Then **Install App** on the repositories Anora Runner may work on; the web app's "add repository" picker lists them.

### 6. Telegram bot and Mini App

1. [@BotFather](https://t.me/BotFather): `/newbot` → `TELEGRAM_BOT_TOKEN`. Find your numeric user id, e.g. with [@userinfobot](https://t.me/userinfobot) → `TELEGRAM_ALLOWED_USER_ID`. The bot and the Mini App accept only this user.
2. `/newapp` → pick the bot, give a title, a short description and a 640×360 photo, then the **Web App URL** `https://anora.example.com` and a short name (e.g. `anora`). BotFather answers with `t.me/<bot>/<short name>`: put it in `TELEGRAM_MINIAPP_LINK` (used for deep links in group topics).
3. Optional: `/mybots` → the bot → *Bot Settings* → *Menu Button* → the same URL, so the chat's menu button opens the app.

The bot uses long polling; no Telegram webhook or domain setting is needed. For one topic per project: create a forum supergroup, add the bot as an admin, open a topic and send `/bind <project>`.

### 7. GitHub secrets (both repositories)

In **anora-runner** and in **anora-client**: *Settings → Environments → New environment* `production`, with the same three secrets:

| Secret | Value |
|---|---|
| `DEPLOY_HOST` | the server's address |
| `DEPLOY_USER` | the deploy user from step 1 |
| `DEPLOY_SSH_KEY` | a private key whose public key is in that user's `~/.ssh/authorized_keys` (e.g. `ssh-keygen -t ed25519 -f anora-deploy -N ''`) |

The images are pushed to GHCR with each repository's `GITHUB_TOKEN`. If the packages (`anora-runner`, `anora-agent`, `anora-client`) are private, log the server in once with a token that has `read:packages`: `echo <token> | docker login ghcr.io -u andrewsl105 --password-stdin`.

### 8. First start

Run both release workflows once (push to `main`, or *Actions → release → Run workflow*) so the images exist; their deploy step can fail on a server that has not been started yet, which is fine. Then:

```sh
cd /srv/anora
docker pull ghcr.io/andrewsl105/anora-agent:latest   # runners are never pulled automatically
docker compose -f docker-compose.deploy.yml up -d
docker compose -f docker-compose.deploy.yml logs -f caddy   # wait for "certificate obtained successfully"
curl -s https://anora.example.com/healthz
```

Open `https://anora.example.com` in a browser and log in with the password and a TOTP code; open the Mini App from the bot (`/app`, or `t.me/<bot>/<short name>`). Migrations run on startup.

### 9. Updates

- **Releases**: each repository's `release.yml` deploys only its own service: anora-runner pulls `anora-agent` and `anora`, then `docker compose up -d --no-deps anora`; anora-client pulls and restarts `anora-client`. Both take a lock on `/srv/anora/.deploy.lock`, so two deploys never run Compose at the same time.
- **Deploy files** (`docker-compose.deploy.yml`, `Caddyfile`, new `.env` variables) are not copied by the workflows: copy them as in step 1 and run `docker compose -f docker-compose.deploy.yml up -d`, which recreates only what changed. After a Caddyfile-only change: `docker compose -f docker-compose.deploy.yml exec caddy caddy reload --config /etc/caddy/Caddyfile`.
- **Upgrading from an external `proxy` network** (older setups): copy the new files, `docker compose -f docker-compose.deploy.yml up -d`, then stop the old reverse proxy and `docker network rm proxy`.

### 10. Backups

Back up Postgres daily with `pg_dump`. Example root crontab line (keeps 14 days):

```
15 3 * * * docker exec anora-postgres pg_dump -U anora -Fc anora > /srv/anora/backups/anora-$(date +\%F).dump && find /srv/anora/backups -name 'anora-*.dump' -mtime +14 -delete
```

Restore with `docker exec -i anora-postgres pg_restore -U anora -d anora --clean < backup.dump`. Workspaces are disposable (the branch lives on GitHub); the database is the state. Keep the `caddy_data` volume too (certificates), or Caddy requests new ones.

## CI/CD

- **`ci.yml`** (pull requests): `go vet`, `go test -p 1 ./...` against a Postgres service, with the fake runner image built for the Docker integration tests; `golangci-lint`; and the runner's `npm ci && npm run typecheck && npm test`.
- **`release.yml`** (push to `main`): builds and pushes `ghcr.io/andrewsl105/anora-runner` and `ghcr.io/andrewsl105/anora-agent` (tags `<sha>` and `latest`, linux/amd64). Then it connects over SSH, takes `/srv/anora/.deploy.lock`, pulls the agent image (Anora Runner never pulls it itself) and the `anora` service, and restarts only `anora` (`up -d --no-deps anora`). Secrets: see Server setup, step 7.

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

### Local stack with the web client

`deploy/docker-compose.dev.yml` runs Anora Runner, Postgres and the Docker socket proxy with the fake runner (`ANORA_RUNNER_FAKE`, no Claude), `COOKIE_INSECURE=true`, no GitHub App and no Telegram bot. It builds `anora-runner:dev` and `anora-agent:dev` from this checkout. Run it from the repository root; workspaces live in `./.dev/workspaces` (same absolute path inside the container).

```sh
docker compose -f deploy/docker-compose.dev.yml build
docker compose -f deploy/docker-compose.dev.yml run --rm --no-deps anora hash-password   # at least 12 characters
docker compose -f deploy/docker-compose.dev.yml run --rm --no-deps anora totp-setup
# deploy/.env.dev (gitignored):
#   ADMIN_PASSWORD_HASH='<hash>'
#   ADMIN_TOTP_SECRET=<secret>
docker compose -f deploy/docker-compose.dev.yml up -d
curl -s localhost:8080/healthz
```

`ANORA_DEV_PORT` (default `8080`) changes the published port and `ANORA_DEV_CLIENT_ORIGIN` (default `http://localhost:5173`) the web client origin, which is allowed for the WebSocket (`CORS_ORIGINS`) and used for links. Then start the client (`anora-client`: `npm run dev`, its Vite proxy targets `http://localhost:8080`) and log in with the password and a code from your authenticator.

Without a GitHub App the "add repository" picker is unavailable, so add a public repository through the API (the clone is anonymous; commits stay local and the task ends `completed`):

```sh
curl -c /tmp/anora.jar -H 'X-Requested-With: anora' -H 'Content-Type: application/json' \
  -d '{"password":"…","totp":"123456"}' localhost:8080/api/v1/auth/login
curl -b /tmp/anora.jar -H 'X-Requested-With: anora' -H 'Content-Type: application/json' \
  -d '{"full_name":"octocat/Hello-World","installation_id":1,"base_branch":"master"}' \
  localhost:8080/api/v1/projects/<project>/repositories
```

Every fake run asks "Which greeting should I write?" (hello/hi), writes `ANORA_FAKE.md`, commits and finishes; an interrupt while it waits ends the run like a real one. Stop with `docker compose -f deploy/docker-compose.dev.yml down` (`-v` also drops the database).

Without Docker Compose: a `.env` based on `deploy/.env.example` with `DOCKER_HOST=unix:///var/run/docker.sock`, `COOKIE_INSECURE=true`, `RUNNER_IMAGE=anora-agent:dev` and `ANORA_RUNNER_FAKE=1`, then `go run ./cmd/anora serve`.
