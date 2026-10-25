# Fleetpocket

A self-hosted, single-owner tool that runs Claude Code sessions in isolated Docker containers against your GitHub repositories and turns the result into a pull request. You drive it from a Telegram bot, or from the web client, which is also a Telegram Mini App.

```
Telegram bot ─┐                        ┌─ GitHub (clone, push, PR, webhooks)
Web/Mini App ─┼─► server (Go) ─────────┤
 (web/)       │   API · bot · scheduler └─ docker-proxy ─► per task: network fleetpocket-task-N
              │   holds every credential                    ├─ agent (Claude Code, /workspace)
              └── postgres                                  └─ postgres / redis sidecars
```

The server owns every credential and side effect. An agent sees only its workspace folder and a prompt: it gets no GitHub token, no Docker access and no host network.

## Layout

```
server/   Go service: REST + WebSocket API, Telegram bot, scheduler, Docker and GitHub   (server/README.md)
agent/    TypeScript agent (Claude Agent SDK) that runs inside each task container      (docs/PROTOCOL.md)
web/      React web client and Telegram Mini App, static files served by nginx          (web/README.md)
deploy/   docker-compose.deploy.yml + Caddyfile (server), docker-compose.dev.yml (local), .env.example
docs/     SPEC-server.md, SPEC-web.md, API.md, PROTOCOL.md, openapi.yaml, DECISIONS.md
```

- The API contract is `docs/openapi.yaml` (behaviour in `docs/API.md`); `web/src/api/schema.ts` is generated from it (`make gen-api`) and CI fails when it is stale.
- Server ↔ agent protocol: `docs/PROTOCOL.md`. Deviations from the specs: `docs/DECISIONS.md`.
- Images: `ghcr.io/andrewsl105/fleetpocket-server`, `fleetpocket-agent`, `fleetpocket-web`.

## Development

Requirements: Go 1.27+, Node 24, Docker, git, gitleaks (`brew install gitleaks`), golangci-lint 2.14.

```sh
make deps        # npm ci in agent/ and web/
make dev-setup   # builds the dev images, prints a password hash and a TOTP secret
```

Put both values in `deploy/.env.dev` (gitignored):

```
ADMIN_PASSWORD_HASH='<hash>'
ADMIN_TOTP_SECRET=<secret>
```

```sh
make dev         # server + postgres + docker-proxy (fake agent) in Docker, then Vite on :5173
```

Open `http://localhost:5173` and log in with the password and a code from your authenticator. The dev stack has no Claude (`FLEETPOCKET_RUNNER_FAKE`), no GitHub App and no Telegram bot: every run asks "Which greeting should I write?" (hello/hi), writes `FLEETPOCKET_FAKE.md`, commits locally and finishes. Without a GitHub App, add a public repository to a project through the API (see `server/README.md`). Ports: `make dev FLEETPOCKET_DEV_PORT=8081 WEB_PORT=5174` when 8080 or 5173 are taken. `make dev-down` stops the stack.

| Command | What it does |
|---|---|
| `make test` | `go test -p 1` (database and Docker integration tests need `make test-db` once, or `FLEETPOCKET_TEST_DATABASE_URL`), agent tests, web unit tests |
| `make lint` | `go vet` + golangci-lint, agent typecheck, web schema check + typecheck + ESLint |
| `make build` | production images, tagged `:dev` |
| `make gen-api` | regenerate `web/src/api/schema.ts` from `docs/openapi.yaml` |
| `make e2e` | Playwright smoke tests in mock mode (no backend) |
| `make e2e-real` | Playwright against the running dev stack (`E2E_PASSWORD`, `E2E_TOTP_SECRET`) |

Every API change goes into `docs/openapi.yaml` and `docs/API.md` in the same commit as the code, with `make gen-api`.

## Server setup

One server, one domain. `/srv/fleetpocket/docker-compose.deploy.yml` runs `caddy` (TLS, the only service with ports), `web` (nginx with the client), `server`, `postgres` and `docker-proxy`. Caddy serves `https://<domain>`: `/api/*` (including the WebSocket `/api/v1/ws`), `/webhooks/*` and `/healthz` go to `server:8080`, everything else to `web:80`; `/metrics` answers 404 from outside. Agent containers are started by the server, each in its own task network.

Target: a fresh Ubuntu 24.04 VPS with Docker Engine and the Compose plugin, nothing else. Open ports 22, 80 and 443 (and 443/udp for HTTP/3). Below, `fleetpocket.example.com` stands for your domain.

**DNS**: create an A record (and AAAA for IPv6) for `fleetpocket.example.com` pointing at the server, and wait until `dig +short fleetpocket.example.com` returns its address before the first start: Caddy needs it to get the certificate.

### 1. Deploy user and directories

The release workflow logs in over SSH as a deploy user that can run Docker. On Ubuntu cloud images the default user (`ubuntu`) already has uid 1000, which is also the uid of the server and the agent containers; any user in the `docker` group works.

```sh
sudo usermod -aG docker "$USER"            # the deploy user; log in again afterwards
sudo mkdir -p /srv/fleetpocket/workspaces /srv/fleetpocket/attachments /srv/fleetpocket/secrets /srv/fleetpocket/backups
sudo chown "$USER": /srv/fleetpocket        # the release workflow copies deploy files here
# The server and the agent containers run as uid 1000 and share the workspaces
# and attachments.
sudo chown -R 1000:1000 /srv/fleetpocket/workspaces /srv/fleetpocket/attachments /srv/fleetpocket/secrets
```

```
/srv/fleetpocket/
├── docker-compose.deploy.yml   copied by the release workflow on every deploy
├── Caddyfile                   copied by the release workflow on every deploy
├── .env                        created by hand (step 2), chmod 600
├── workspaces/                 task checkouts, uid 1000
├── attachments/                images attached to tasks, one folder per task, uid 1000
├── secrets/github-app.pem      GitHub App key, uid 1000, chmod 600
└── backups/                    pg_dump files (step 10)
```

No source code goes on the server. Create `.env` from the example: `scp deploy/.env.example <user>@<server>:/srv/fleetpocket/.env`. The compose file creates its own networks (`fleetpocket_stack`, `fleetpocket_internal`); there is no `docker network create` step and no other reverse proxy.

### 2. `.env`

Fill in `/srv/fleetpocket/.env` (every variable is described in `deploy/.env.example`). The ones that must be set:

| Variable | Value |
|---|---|
| `FLEETPOCKET_DOMAIN` | `fleetpocket.example.com` (Caddy's site address) |
| `PUBLIC_BASE_URL` | `https://fleetpocket.example.com` |
| `WEB_APP_URL` | `https://fleetpocket.example.com` (the client is on the same domain) |
| `PG_PASSWORD` | `openssl rand -hex 24` |
| `SESSION_SECRET` | `openssl rand -hex 32` |
| `ADMIN_PASSWORD_HASH`, `ADMIN_TOTP_SECRET` | step 3 |
| `CLAUDE_CODE_OAUTH_TOKEN` (or `ANTHROPIC_API_KEY`) | step 4 |
| `GITHUB_APP_ID`, `GITHUB_WEBHOOK_SECRET` | step 5 |
| `TELEGRAM_BOT_TOKEN`, `TELEGRAM_ALLOWED_USER_ID`, `TELEGRAM_MINIAPP_LINK` | step 6 |
| `STT_PROVIDER` + provider credentials (optional) | step 6 |

Leave `COOKIE_INSECURE=false`, `CORS_ORIGINS` empty (same origin), `FLEETPOCKET_RUNNER_FAKE=false`, `WORKSPACES_DIR=/srv/fleetpocket/workspaces` and `ATTACHMENTS_DIR=/srv/fleetpocket/attachments`. `chmod 600 /srv/fleetpocket/.env`.

### 3. Owner login (password + TOTP)

These commands need the image: run the release workflow once first (step 8; for private packages, `docker login ghcr.io` as in step 7). Without it: `make dev-setup` on your machine prints the same values.

```sh
docker run --rm -it ghcr.io/andrewsl105/fleetpocket-server:latest hash-password   # prints ADMIN_PASSWORD_HASH (at least 12 characters)
docker run --rm ghcr.io/andrewsl105/fleetpocket-server:latest totp-setup          # prints ADMIN_TOTP_SECRET and an otpauth:// URI
```

Add the `otpauth://` URI to your authenticator app (paste it, or render it as a QR code, e.g. `qrencode -t ansiutf8 '<uri>'`). Quote the bcrypt hash with single quotes in `.env`, because it contains `$`.

### 4. Claude auth

Run `claude setup-token` on your machine and put the result in `CLAUDE_CODE_OAUTH_TOKEN`. Alternatively set `ANTHROPIC_API_KEY`. Only runner containers receive it.

### 5. GitHub App

On GitHub: **Settings → Developer settings → GitHub Apps → New GitHub App** (on your personal account).

- **Homepage URL**: `https://fleetpocket.example.com`.
- **Webhook URL**: `https://fleetpocket.example.com/webhooks/github`; **Webhook secret**: `openssl rand -hex 32` (put it in `GITHUB_WEBHOOK_SECRET`).
- **Repository permissions**: Contents *read & write*, Pull requests *read & write*, Metadata *read*, Checks *read*, Actions *read*.
- **Subscribe to events**: Pull request, Check suite, Workflow run.
- **Where can this App be installed**: Only on this account.

After creating it, note the **App ID** (`GITHUB_APP_ID`), generate a **private key**, and save it as `/srv/fleetpocket/secrets/github-app.pem` (`sudo chown 1000:1000` and `chmod 600`). Then **Install App** on the repositories Fleetpocket may work on; the web app's "add repository" picker lists them.

### 6. Telegram bot and Mini App

1. [@BotFather](https://t.me/BotFather): `/newbot` → `TELEGRAM_BOT_TOKEN`. Find your numeric user id, e.g. with [@userinfobot](https://t.me/userinfobot) → `TELEGRAM_ALLOWED_USER_ID`. The bot and the Mini App accept only this user.
2. `/newapp` → pick the bot, give a title, a short description and a 640×360 photo, then the **Web App URL** `https://fleetpocket.example.com` and a short name (e.g. `fleetpocket`). BotFather answers with `t.me/<bot>/<short name>`: put it in `TELEGRAM_MINIAPP_LINK` (used for deep links in group topics).
3. The service registers the command list and configures the bot's menu button to open `WEB_APP_URL` at startup.

The bot uses long polling; no Telegram webhook or domain setting is needed. For one topic per project: create a forum supergroup, add the bot as an admin, open a topic and send `/bind <project>`. Plain owner messages create a 24-hour draft; messages sent within three seconds are merged. Reply to any task card, question, or notification to answer the open question or send a follow-up. Photos (or images sent as files) go along: as a draft's images, as the images of a follow-up when they reply to a task message, or with `/new` as the caption. An album counts as one message. Images without a caption get a prompt asking what Claude should do; reply to it with the text. The Bot API downloads files of at most 20 MB, so larger images have to go through the web app.

Voice messages are disabled by default. Set `STT_PROVIDER=openai` and `OPENAI_API_KEY` to use the file transcription API. The transcript is shown before task creation and echoed when voice is sent as a task reply. The OpenAI adapter uses `gpt-transcribe` and accepts Ukrainian and English without forcing a language.

For a local sidecar, run the `whisper-server` binary from whisper.cpp with `--host 0.0.0.0 --convert --language auto`, keep it on a Docker network reachable only by the server, then set `STT_PROVIDER=whispercpp` and `WHISPERCPP_URL=http://whispercpp:8080`. Fleetpocket posts multipart audio to its `/inference` endpoint; `--convert` is required for Telegram's OGG and other non-WAV audio.

### 7. GitHub secrets

In **andrewsl105/fleetpocket**: *Settings → Environments → New environment* `production`, with three secrets:

| Secret | Value |
|---|---|
| `DEPLOY_HOST` | the server's address |
| `DEPLOY_USER` | the deploy user from step 1 |
| `DEPLOY_SSH_KEY` | a private key whose public key is in that user's `~/.ssh/authorized_keys` (e.g. `ssh-keygen -t ed25519 -f fleetpocket-deploy -N ''`) |

Then *Settings → Secrets and variables → Actions → Variables*: add `DEPLOY_ENABLED` = `true`. Until it is set, the release workflow builds and pushes images but skips the deploy job.

The images are pushed to GHCR with the repository's `GITHUB_TOKEN`. If the packages (`fleetpocket-server`, `fleetpocket-agent`, `fleetpocket-web`) are private, log the server in once with a token that has `read:packages`: `echo <token> | docker login ghcr.io -u andrewsl105 --password-stdin`.

### 8. First start

Run the release workflow by hand once (*Actions → release → Run workflow*): it builds all three images and copies the deploy files. Its last step starts the stack; if `.env` was not ready yet, fill it in and start it yourself:

```sh
cd /srv/fleetpocket
docker pull ghcr.io/andrewsl105/fleetpocket-agent:latest   # runners are never pulled automatically
docker compose -f docker-compose.deploy.yml up -d
docker compose -f docker-compose.deploy.yml logs -f caddy   # wait for "certificate obtained successfully"
curl -s https://fleetpocket.example.com/healthz
```

Open `https://fleetpocket.example.com` in a browser and log in with the password and a TOTP code; open the Mini App from the bot (`/app`, or `t.me/<bot>/<short name>`). Migrations run on startup.

### 9. Updates

- **Releases**: every push to `main` builds only the images whose directories changed (`server/`, `agent/`, `web/`), copies `deploy/docker-compose.deploy.yml` and `deploy/Caddyfile` to `/srv/fleetpocket`, pulls the changed images and always `fleetpocket-agent` (the server never pulls it), runs `docker compose up -d` (recreates only what changed) and reloads Caddy.
- **New `.env` variables** are added by hand, then `docker compose -f docker-compose.deploy.yml up -d`.

### 10. Backups

Back up Postgres daily with `pg_dump`. Example root crontab line (keeps 14 days):

```
15 3 * * * docker compose -f /srv/fleetpocket/docker-compose.deploy.yml exec -T postgres pg_dump -U fleetpocket -Fc fleetpocket > /srv/fleetpocket/backups/fleetpocket-$(date +\%F).dump && find /srv/fleetpocket/backups -name 'fleetpocket-*.dump' -mtime +14 -delete
```

Restore with `docker compose -f docker-compose.deploy.yml exec -T postgres pg_restore -U fleetpocket -d fleetpocket --clean < backup.dump`. Workspaces are disposable (the branch lives on GitHub); the database is the state. Keep the `caddy_data` volume too (certificates), or Caddy requests new ones.

### 11. Migrating from Anora

An install from before the rename keeps everything under the old name: `/srv/anora`, the compose project `anora` (volumes `anora_pgdata`, `anora_caddy_data`, `anora_caddy_config`), the Postgres role and database `anora`, `ANORA_DOMAIN` in `.env`, and `anora.*` labels on agent containers. Migrate once, **before** pushing the rename to `main`, because the release workflow deploys to `/srv/fleetpocket`. The web app logs you out (new cookie name) and forgets the theme and new-task drafts (new localStorage keys).

```sh
# 1. Back up and stop the old stack, and remove leftover agent containers, networks and volumes.
cd /srv/anora
docker compose -f docker-compose.deploy.yml exec -T postgres pg_dump -U anora -Fc anora > backups/pre-rename.dump
docker compose -f docker-compose.deploy.yml down
docker ps -aq --filter label=anora.task_id | xargs -r docker rm -f
docker network ls -q --filter label=anora.task_id | xargs -r docker network rm
docker volume ls -q --filter label=anora.task_id | xargs -r docker volume rm

# 2. Move the directory; rename the variable, paths and per-workspace event logs.
sudo mv /srv/anora /srv/fleetpocket
cd /srv/fleetpocket
sed -i 's/^ANORA_/FLEETPOCKET_/; s|/srv/anora/|/srv/fleetpocket/|g' .env
for d in workspaces/*/; do [ -d "$d.anora" ] && sudo mv "$d.anora" "$d.fleetpocket"; done

# 3. Copy the volumes to the new project's names.
for v in pgdata caddy_data caddy_config; do
  docker volume create "fleetpocket_$v"
  docker run --rm -v "anora_$v:/from:ro" -v "fleetpocket_$v:/to" alpine cp -a /from/. /to/
done

# 4. Rename the Postgres role and database (POSTGRES_USER/POSTGRES_DB apply only to an empty volume).
#    First copy the new deploy files from your machine:
#    scp deploy/docker-compose.deploy.yml deploy/Caddyfile <user>@<server>:/srv/fleetpocket/
compose="docker compose -f docker-compose.deploy.yml"
$compose up -d postgres
$compose exec -T postgres psql -U anora -d postgres -c 'CREATE ROLE rename_tmp SUPERUSER LOGIN'
$compose exec -T postgres psql -U rename_tmp -d postgres -c 'ALTER ROLE anora RENAME TO fleetpocket' -c 'ALTER DATABASE anora RENAME TO fleetpocket'
$compose exec -T postgres psql -U fleetpocket -d postgres -c 'DROP ROLE rename_tmp'
$compose down
```

Then push to `main` (the release workflow builds the `fleetpocket-*` images, copies the deploy files and starts the stack), or run it by hand. Once it is up, remove the old volumes (`docker volume rm anora_pgdata anora_caddy_data anora_caddy_config`) and images. Outside the server: rename the bot, Mini App and its short name in @BotFather and the GitHub App if you want the new name there too; change the domain only if you also update DNS, `.env` and the GitHub App URLs.

## CI/CD

- **`ci.yml`** (pull requests and `main`): path-filtered jobs. `server/**`, `agent/**` or `docs/openapi.yaml` → `go vet`, `go test -p 1` against Postgres with the fake agent image, golangci-lint. `agent/**` → typecheck and tests. `web/**` or `docs/openapi.yaml` → schema check (fails if `schema.ts` is stale), typecheck, lint, unit tests, build, bundle budgets, Playwright (mock mode). `deploy/**` → compose files and Caddyfile validate.
- **`release.yml`** (push to `main`, or by hand for everything): builds and pushes only the changed images (`<sha>` and `latest`, linux/amd64), copies the deploy files to `/srv/fleetpocket`, pulls the changed images and always the agent image, `docker compose up -d`, reloads Caddy.

## Operations

- **Health**: `GET /healthz` checks Postgres and Docker.
- **Metrics** (`GET /metrics`, internal network only): `fleetpocket_tasks{status}`, `fleetpocket_queue_length`, `fleetpocket_run_duration_seconds{project}`, `fleetpocket_cost_usd_total{project}`, `fleetpocket_runner_exits_total{code}`, `fleetpocket_telegram_api_errors_total{op}`, `fleetpocket_github_api_errors_total{op}`.
- **Logs**: `docker compose -f docker-compose.deploy.yml logs -f server`; JSON (`log/slog`), task lines carry `task_id`, `run_id` and `project`.
- **Retention** (every 10 minutes): agent events of finished tasks older than `EVENT_RETENTION` are deleted (status events stay); workspaces, Claude state volumes and the attachments of failed and cancelled tasks are deleted `WORKSPACE_RETENTION` after a task finishes (a completed task's attachments go right away); attachment files without a task are swept; idle sidecars are removed after `SIDECAR_IDLE_TTL`; expired sessions are purged.
- **Restart safety**: on startup, running agents are re-attached and missed events replayed from `<workspace>/.fleetpocket/events.jsonl`; exits that happened while the server was down are applied, vanished runs fail with reason `lost`, interrupted preparation is re-queued, interrupted pushes retried, orphaned containers and networks removed.
