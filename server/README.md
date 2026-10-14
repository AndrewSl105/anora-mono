# Anora Runner

Self-hosted Go service that runs Claude Code sessions in isolated Docker containers against GitHub repositories and turns the result into a pull request. Single owner; driven from a Telegram bot and a separate web client / Telegram Mini App over REST + WebSocket.

- Full specification: [docs/SPEC.md](docs/SPEC.md)
- Deviations from the spec: [docs/DECISIONS.md](docs/DECISIONS.md)

## Status

Scaffold only. Implementation follows the eight phases in SPEC section 10:

1. Runner core
2. Backend skeleton
3. Execution with the fake runner
4. GitHub App
5. Real Claude
6. Real-time and Telegram
7. Operations
8. CI/CD

## Layout

```
cmd/anora/        Go entrypoint
internal/         Go packages (config, store, projects, tasks, scheduler, workspace,
                  githubapp, runnermgr, events, api, auth, telegram, maintenance)
runner/           TypeScript runner + Dockerfile
deploy/           docker-compose.deploy.yml, .env.example
docs/             SPEC.md, API.md, openapi.yaml, PROTOCOL.md, DECISIONS.md
.github/workflows CI and release
```
