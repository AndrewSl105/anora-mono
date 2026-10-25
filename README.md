# Anora

A self-hosted, single-owner tool that runs Claude Code sessions in isolated Docker containers against your GitHub repositories and turns the result into a pull request. You drive it from a Telegram bot, or from the web client, which is also a Telegram Mini App.

```
Telegram bot ─┐                        ┌─ GitHub (clone, push, PR, webhooks)
Web/Mini App ─┼─► server (Go) ─────────┤
 (web/)       │   API · bot · scheduler └─ docker-proxy ─► per task: network anora-task-N
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
- Images: `ghcr.io/andrewsl105/anora-server`, `anora-agent`, `anora-web`.

## Documentation

- [Development](docs/DEVELOPMENT.md): local dev stack, tests, lint, codegen.
- [Deployment](docs/DEPLOY.md): server setup (steps 1–10) and CI/CD.
- [Operations](docs/OPERATIONS.md): health, metrics, logs, retention, restart safety.
