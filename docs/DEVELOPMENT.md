# Development

Local setup for working on Anora. Back to the [README](../README.md).


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

Open `http://localhost:5173` and log in with the password and a code from your authenticator. The dev stack has no Claude (`ANORA_RUNNER_FAKE`), no GitHub App and no Telegram bot: every run asks "Which greeting should I write?" (hello/hi), writes `ANORA_FAKE.md`, commits locally and finishes. Without a GitHub App, add a public repository to a project through the API (see [server/README.md](../server/README.md#local-development)). Ports: `make dev ANORA_DEV_PORT=8081 WEB_PORT=5174` when 8080 or 5173 are taken. `make dev-down` stops the stack.

| Command | What it does |
|---|---|
| `make test` | `go test -p 1` (database and Docker integration tests need `make test-db` once, or `ANORA_TEST_DATABASE_URL`), agent tests, web unit tests |
| `make lint` | `go vet` + golangci-lint, agent typecheck, web schema check + typecheck + ESLint |
| `make build` | production images, tagged `:dev` |
| `make gen-api` | regenerate `web/src/api/schema.ts` from `docs/openapi.yaml` |
| `make e2e` | Playwright smoke tests in mock mode (no backend) |
| `make e2e-real` | Playwright against the running dev stack (`E2E_PASSWORD`, `E2E_TOTP_SECRET`) |
| `make screenshots` | README screenshots and `demo.gif` in `docs/screenshots` (mock mode; the GIF needs `ffmpeg`) |

Every API change goes into `docs/openapi.yaml` and `docs/API.md` in the same commit as the code, with `make gen-api`.
