# Fleetpocket

Monorepo with three parts that change together:

- `server/` — Go service (module `github.com/andrewsl105/fleetpocket/server`): REST + WebSocket API, Telegram bot, scheduler, Docker (via docker-socket-proxy) and GitHub App. Spec: `docs/SPEC-server.md`.
- `agent/` — TypeScript agent (Claude Agent SDK) that runs inside each task container and talks to the server over stdin/stdout (`docs/PROTOCOL.md`; source of truth `agent/src/protocol.ts`, Go mirror `server/internal/runnermgr/protocol.go`). Spec: `docs/SPEC-server.md` section 4.
- `web/` — React + Vite web client and Telegram Mini App. Spec: `docs/SPEC-web.md`.

Contract: `docs/openapi.yaml` + `docs/API.md` (one copy; `web/src/api/schema.ts` is generated from it). Deviations from the specs: `docs/DECISIONS.md`, one section per part.

## Commands

| | |
|---|---|
| `make dev` / `make dev-down` | dev stack (`deploy/docker-compose.dev.yml`: server, postgres, docker-proxy, fake agent) + Vite |
| `make test` | `cd server && go test -p 1 ./...` (needs `FLEETPOCKET_TEST_DATABASE_URL`, e.g. `make test-db`, and the `fleetpocket-agent:dev` image, which the target builds), `cd agent && npm test`, `cd web && npm test` |
| `make lint` | `go vet` + `golangci-lint run` in `server/`, `npm run typecheck` in `agent/`, `npm run check:api && npm run typecheck && npm run lint` in `web/` |
| `make gen-api` | regenerate `web/src/api/schema.ts` after changing `docs/openapi.yaml` |
| `make e2e` / `make e2e-real` | Playwright in mock mode / against the running dev stack |
| `make build` | `fleetpocket-server`, `fleetpocket-agent`, `fleetpocket-web` images |

## Rules

- Verify library, SDK and Telegram WebApp APIs against installed versions and current docs; never invent option names, endpoints, fields or states.
- Record every deviation from a spec in `docs/DECISIONS.md` (in the part's section) with one line of reasoning.
- Keep `docs/PROTOCOL.md`, `docs/API.md` and `docs/openapi.yaml` in the same commit as the code they describe, and run `make gen-api` in that commit for any API change.
- Security properties in SPEC-server sections 3 and 5 are requirements: the agent gets no GitHub token, no Docker access, no host network; tokens never touch disk.
- Web: mobile first (Telegram Mini App at 375 px), no hand-written API types, all API calls through `web/src/api/client.ts` (it adds the CSRF header and, in Telegram mode, the bearer token).
- Small, focused commits.
