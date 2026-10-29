# Deployment

How to run Fleetpocket on one server, and how CI/CD builds and deploys it. Back to the [README](../README.md); day-to-day operations are in [OPERATIONS.md](OPERATIONS.md).

One server, one domain. `/srv/fleetpocket/docker-compose.deploy.yml` runs `caddy` (TLS, the only service with ports), `web` (nginx with the client), `server`, `postgres` and `docker-proxy`. Caddy serves `https://<domain>`: `/api/*` (including the WebSocket `/api/v1/ws`), `/webhooks/*` and `/healthz` go to `server:8080`, everything else to `web:80`; `/metrics` answers 404 from outside. Agent containers are started by the server, each in its own task network.

Target: a fresh Ubuntu 24.04 VPS with Docker Engine and the Compose plugin, nothing else. Open ports 22, 80 and 443 (and 443/udp for HTTP/3). Below, `fleetpocket.example.com` stands for your domain.

**Two ways to deploy.** With the published images (`ghcr.io/andrewsl105/fleetpocket-*`, public), follow steps 1–6, download `deploy/docker-compose.deploy.yml`, `deploy/Caddyfile` and `deploy/.env.example` (as `.env`) into `/srv/fleetpocket` as in the [README](../README.md#set-it-up), and start the stack as in step 8; skip step 7. Steps 7–9 set up automatic deploys from the repository with GitHub Actions; they use the repository's own secrets and image names.

**DNS**: create an A record (and AAAA for IPv6) for `fleetpocket.example.com` pointing at the server, and wait until `dig +short fleetpocket.example.com` returns its address before the first start: Caddy needs it to get the certificate.

## 1. Deploy user and directories

The release workflow logs in over SSH as a deploy user that can run Docker. On Ubuntu cloud images the default user (`ubuntu`) already has uid 1000, which is also the uid of the server and the agent containers; any user in the `docker` group works.

```sh
sudo usermod -aG docker "$USER"            # the deploy user; log in again afterwards
sudo mkdir -p /srv/fleetpocket/workspaces /srv/fleetpocket/attachments /srv/fleetpocket/secrets /srv/fleetpocket/backups
sudo chown "$USER": /srv/fleetpocket              # the release workflow copies deploy files here
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

## 2. `.env`

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
| `OPENAI_API_KEY` (Codex), `CURSOR_API_KEY` (Cursor), `DEFAULT_AGENT` (optional) | step 4 |
| `GITHUB_APP_ID`, `GITHUB_WEBHOOK_SECRET` | step 5 |
| `TELEGRAM_BOT_TOKEN`, `TELEGRAM_ALLOWED_USER_ID`, `TELEGRAM_MINIAPP_LINK` | step 6 |
| `STT_PROVIDER` + provider credentials (optional) | step 6 |

Leave `COOKIE_INSECURE=false`, `CORS_ORIGINS` empty (same origin), `FLEETPOCKET_RUNNER_FAKE=false`, `WORKSPACES_DIR=/srv/fleetpocket/workspaces` and `ATTACHMENTS_DIR=/srv/fleetpocket/attachments`. `chmod 600 /srv/fleetpocket/.env`.

## 3. Owner login (password + TOTP)

These commands pull the published server image. With your own private images, run the release workflow once first (step 8) and `docker login ghcr.io` as in step 7. `make dev-setup` on your machine prints the same values.

```sh
docker run --rm -it ghcr.io/andrewsl105/fleetpocket-server:latest hash-password   # prints ADMIN_PASSWORD_HASH (at least 12 characters)
docker run --rm ghcr.io/andrewsl105/fleetpocket-server:latest totp-setup          # prints ADMIN_TOTP_SECRET and an otpauth:// URI
```

Add the `otpauth://` URI to your authenticator app (paste it, or render it as a QR code, e.g. `qrencode -t ansiutf8 '<uri>'`). Quote the bcrypt hash with single quotes in `.env`, because it contains `$`.

## 4. Claude auth

Run `claude setup-token` on your machine and put the result in `CLAUDE_CODE_OAUTH_TOKEN`. Alternatively set `ANTHROPIC_API_KEY`. Only runner containers receive it.

**Codex and Cursor (optional).** Tasks can also run with OpenAI Codex or Cursor. Set `OPENAI_API_KEY` (an OpenAI API key; ChatGPT sign-in is not supported) for Codex and `CURSOR_API_KEY` (an API key from your Cursor dashboard) for Cursor. Pick the agent per project in its settings, or per task when you create it; `DEFAULT_AGENT` sets the agent for projects that pick none. A runner container only receives the key of its task's agent. Codex and Cursor never ask questions mid-run, report no cost (the budget limit does not apply to them) and enforce only the time limit.

## 5. GitHub App

On GitHub: **Settings → Developer settings → GitHub Apps → New GitHub App** (on your personal account).

- **Homepage URL**: `https://fleetpocket.example.com`.
- **Webhook URL**: `https://fleetpocket.example.com/webhooks/github`; **Webhook secret**: `openssl rand -hex 32` (put it in `GITHUB_WEBHOOK_SECRET`).
- **Repository permissions**: Contents *read & write*, Pull requests *read & write*, Metadata *read*, Checks *read*, Actions *read*.
- **Subscribe to events**: Pull request, Check suite, Workflow run.
- **Where can this App be installed**: Only on this account.

After creating it, note the **App ID** (`GITHUB_APP_ID`), generate a **private key**, and save it as `/srv/fleetpocket/secrets/github-app.pem` (`sudo chown 1000:1000` and `chmod 600`). Then **Install App** on the repositories Fleetpocket may work on; the web app's "add repository" picker lists them.

## 6. Telegram bot and Mini App

1. [@BotFather](https://t.me/BotFather): `/newbot` → `TELEGRAM_BOT_TOKEN`. Find your numeric user id, e.g. with [@userinfobot](https://t.me/userinfobot) → `TELEGRAM_ALLOWED_USER_ID`. The bot and the Mini App accept only this user.
2. `/newapp` → pick the bot, give a title, a short description and a 640×360 photo, then the **Web App URL** `https://fleetpocket.example.com` and a short name (e.g. `fleetpocket`). BotFather answers with `t.me/<bot>/<short name>`: put it in `TELEGRAM_MINIAPP_LINK` (used for deep links in group topics).
3. The service registers the command list and configures the bot's menu button to open `WEB_APP_URL` at startup.

The bot uses long polling; no Telegram webhook or domain setting is needed. For one topic per project: create a forum supergroup, add the bot as an admin, open a topic and send `/bind <project>`. Plain owner messages create a 24-hour draft; messages sent within three seconds are merged. Reply to any task card, question, or notification to answer the open question or send a follow-up. Photos (or images sent as files) go along: as a draft's images, as the images of a follow-up when they reply to a task message, or with `/new` as the caption. An album counts as one message. Images without a caption get a prompt asking what Claude should do; reply to it with the text. The Bot API downloads files of at most 20 MB, so larger images have to go through the web app.

Voice messages are disabled by default. Set `STT_PROVIDER=openai` and `OPENAI_API_KEY` to use the file transcription API. The transcript is shown before task creation and echoed when voice is sent as a task reply. The OpenAI adapter uses `gpt-transcribe` and accepts Ukrainian and English without forcing a language.

For a local sidecar, run the `whisper-server` binary from whisper.cpp with `--host 0.0.0.0 --convert --language auto`, keep it on a Docker network reachable only by the server, then set `STT_PROVIDER=whispercpp` and `WHISPERCPP_URL=http://whispercpp:8080`. Fleetpocket posts multipart audio to its `/inference` endpoint; `--convert` is required for Telegram's OGG and other non-WAV audio.

## 7. GitHub secrets

In **AndrewSl105/fleetpocket**: *Settings → Environments → New environment* `production`, with three secrets:

| Secret | Value |
|---|---|
| `DEPLOY_HOST` | the server's address |
| `DEPLOY_USER` | the deploy user from step 1 |
| `DEPLOY_SSH_KEY` | a private key whose public key is in that user's `~/.ssh/authorized_keys` (e.g. `ssh-keygen -t ed25519 -f fleetpocket-deploy -N ''`) |

Then *Settings → Secrets and variables → Actions → Variables*: add `DEPLOY_ENABLED` = `true`. Until it is set, the release workflow builds and pushes images but skips the deploy job.

The images are pushed to GHCR with the repository's `GITHUB_TOKEN`. If the packages (`fleetpocket-server`, `fleetpocket-agent`, `fleetpocket-web`) are private, log the server in once with a token that has `read:packages`: `echo <token> | docker login ghcr.io -u andrewsl105 --password-stdin`.

## 8. First start

Run the release workflow by hand once (*Actions → release → Run workflow*): it builds all three images and copies the deploy files. Its last step starts the stack; if `.env` was not ready yet, fill it in and start it yourself:

```sh
cd /srv/fleetpocket
docker pull ghcr.io/andrewsl105/fleetpocket-agent:latest   # runners are never pulled automatically
docker compose -f docker-compose.deploy.yml up -d
docker compose -f docker-compose.deploy.yml logs -f caddy   # wait for "certificate obtained successfully"
curl -s https://fleetpocket.example.com/healthz
```

Open `https://fleetpocket.example.com` in a browser and log in with the password and a TOTP code; open the Mini App from the bot (`/app`, or `t.me/<bot>/<short name>`). Migrations run on startup.

## 9. Updates

- **Releases**: every push to `main` builds only the images whose directories changed (`server/`, `agent/`, `web/`), copies `deploy/docker-compose.deploy.yml` and `deploy/Caddyfile` to `/srv/fleetpocket`, pulls the changed images and always `fleetpocket-agent` (the server never pulls it), runs `docker compose up -d` (recreates only what changed) and reloads Caddy.
- **New `.env` variables** are added by hand, then `docker compose -f docker-compose.deploy.yml up -d`.

## 10. Backups

Back up Postgres daily with `pg_dump`. Example root crontab line (keeps 14 days):

```
15 3 * * * docker compose -f /srv/fleetpocket/docker-compose.deploy.yml exec -T postgres pg_dump -U fleetpocket -Fc fleetpocket > /srv/fleetpocket/backups/fleetpocket-$(date +\%F).dump && find /srv/fleetpocket/backups -name 'fleetpocket-*.dump' -mtime +14 -delete
```

Restore with `docker compose -f docker-compose.deploy.yml exec -T postgres pg_restore -U fleetpocket -d fleetpocket --clean < backup.dump`. Workspaces are disposable (the branch lives on GitHub); the database is the state. Keep the `caddy_data` volume too (certificates), or Caddy requests new ones.

## 11. Run tasks on your own computer (optional)

A **node** runs tasks on another machine, typically your Mac, with that machine's Docker and its own agent keys. Login, history, the task list and GitHub stay on the server; you pick per project or per task where it runs. The node only connects out (a WebSocket to the server), so it needs no open port or public address. Design: [remote-nodes.md](design/remote-nodes.md).

1. In the web app: **Settings → Computers → Add computer**. It shows one command.
2. Run it in Terminal on the computer you want to add:

   ```sh
   curl -fsSL https://fleetpocket.example.com/api/v1/node/install | sh
   ```

   It installs `fleetpocket-node` into `~/.fleetpocket/bin` (no sudo) and runs `fleetpocket-node setup`, which:
   - starts Docker, or offers to install OrbStack with Homebrew, and waits until it runs;
   - connects Claude (`claude setup-token`, or an API key) and optionally Codex and Cursor; keys go to the macOS Keychain;
   - shows a pairing code and opens the app with it. In the app on that computer the dialog shows **This computer → Connect** (one click); on another device type the code;
   - pulls the agent image, offers gitleaks, and starts the node now and at every login (launchd; logs in `~/Library/Logs/fleetpocket-node.log`).
3. The computer turns **online** in Settings → Computers, with the agents it has keys for.

Run setup again any time to add an agent key or to re-pair. By hand (no pairing): Add computer → "Set up by hand with a token", then `fleetpocket-node run --server …` with the token in the Keychain (`security add-generic-password -U -s fleetpocket-node -a FLEETPOCKET_NODE_TOKEN -w`). `NODE_DOWNLOAD_URL` changes where the install script downloads the binaries (default: the `node-latest` release).

While a task runs, the node keeps the Mac awake (`caffeinate -i`); if the Mac sleeps or goes offline anyway, queued tasks wait, a running task keeps running in Docker, and its output catches up when the node reconnects. Workspaces live in `~/Library/Application Support/Fleetpocket`.

## CI/CD

- **`ci.yml`** (every pull request, pushes to `main` and manual runs; a manual run starts every job): path-filtered jobs. `server/**`, `agent/**` or `docs/openapi.yaml` → `go vet`, `go test -p 1` against Postgres with the fake agent image, golangci-lint. `agent/**` → typecheck and tests. `web/**` or `docs/openapi.yaml` → schema check (fails if `schema.ts` is stale), typecheck, lint, unit tests, build, bundle budgets, Playwright (mock mode). `deploy/**` → compose files and Caddyfile validate.
- **`release.yml`** (push to `main`, or by hand for everything): builds and pushes only the changed images (`<sha>` and `latest`, linux/amd64), copies the deploy files to `/srv/fleetpocket`, pulls the changed images and always the agent image, `docker compose up -d`, reloads Caddy.
