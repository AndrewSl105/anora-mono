# Anora server

The Go service of [Anora](../README.md): REST + WebSocket API, Telegram bot, scheduler, Docker orchestration through the socket proxy, and the GitHub App. CLI: `anora serve`, `migrate`, `hash-password`, `totp-setup`.

- Spec: [docs/SPEC-server.md](../docs/SPEC-server.md) · API: [docs/API.md](../docs/API.md), [docs/openapi.yaml](../docs/openapi.yaml) · agent protocol: [docs/PROTOCOL.md](../docs/PROTOCOL.md) · decisions: [docs/DECISIONS.md](../docs/DECISIONS.md)
- Deployment and server setup: [README.md](../README.md#server-setup)

## Packages

```
cmd/anora/            main: serve, migrate, hash-password, totp-setup
internal/config       env parsing and defaults
internal/store        SQL (pgx) and embedded goose migrations
internal/projects     project + repository registry
internal/tasks        task model, state machine, task service (shared by API and bot)
internal/scheduler    queue, concurrency slots, run supervision, startup reconciliation
internal/runnermgr    Docker: networks, sidecars, volumes, agent containers, stdio protocol
internal/workspace    git clone/branch/diff/push, diff validation, secrets scan
internal/githubapp    App auth, installation tokens, PRs, webhooks
internal/events       in-process pub/sub
internal/api          REST, WebSocket, middleware
internal/auth         sessions, password + TOTP, Telegram initData
internal/telegram     the bot
internal/maintenance  retention, workspace cleanup, idle sidecars
internal/metrics      Prometheus
```

## Tests

```sh
make test-db                 # once: Postgres on localhost:55432 (from the repository root)
make test-server             # builds anora-agent:dev, then go test -p 1 ./...
```

Database tests need `ANORA_TEST_DATABASE_URL` (the Makefile sets it for `make test-db`); without it they are skipped. The Docker integration tests run the fake agent image `anora-agent:dev` and must not run in parallel (`-p 1`), because each scheduler removes containers its database doesn't know about.

## Local development

`make dev` (repository root) runs `deploy/docker-compose.dev.yml`: the server, Postgres and the Docker socket proxy with the fake agent (`ANORA_RUNNER_FAKE`, no Claude), `COOKIE_INSECURE=true`, no GitHub App and no Telegram bot, plus the web client's Vite dev server. Workspaces live in `./.dev/workspaces` and attachments in `./.dev/attachments` (same absolute paths inside the container). `ANORA_DEV_PORT` (default `8080`) changes the published port and `ANORA_DEV_CLIENT_ORIGIN` (default `http://localhost:5173`) the web origin, which is allowed for the WebSocket (`CORS_ORIGINS`) and used for links.

Without a GitHub App the "add repository" picker is unavailable, so add a public repository through the API (the clone is anonymous; commits stay local and the task ends `completed`):

```sh
curl -c /tmp/anora.jar -H 'X-Requested-With: anora' -H 'Content-Type: application/json' \
  -d '{"password":"…","totp":"123456"}' localhost:8080/api/v1/auth/login
curl -b /tmp/anora.jar -H 'X-Requested-With: anora' -H 'Content-Type: application/json' \
  -d '{"full_name":"octocat/Hello-World","installation_id":1,"base_branch":"master"}' \
  localhost:8080/api/v1/projects/<project>/repositories
```

Without Docker Compose: a `.env` based on `deploy/.env.example` with `DOCKER_HOST=unix:///var/run/docker.sock`, `COOKIE_INSECURE=true`, `RUNNER_IMAGE=anora-agent:dev` and `ANORA_RUNNER_FAKE=1`, then `go run ./cmd/anora serve` in `server/`.
