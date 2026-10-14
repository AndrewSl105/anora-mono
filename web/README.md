# Anora Client

Web frontend for [Anora Runner](../anora-runner): a Telegram Mini App on the phone and a normal website on desktop. Static SPA (React, TypeScript, Vite, Tailwind, shadcn/ui); all data goes through the Anora Runner REST API and WebSocket on the same domain.

- Spec: [`docs/SPEC.md`](docs/SPEC.md) · backend contract: [`docs/backend/`](docs/backend/) · deviations and backend gaps: [`docs/DECISIONS.md`](docs/DECISIONS.md)

## Local development

```sh
npm ci
cp .env.example .env.local   # set ANORA_BACKEND_URL
npm run dev                  # http://localhost:5173, proxies /api, /api/v1/ws, /healthz
```

### Mock mode (no backend)

```sh
npm run dev:mock
```

MSW serves the whole API from memory and a fake WebSocket streams events for the running task. Sign in with password `anora` and any 6-digit code except `000000`. Add `?tg=1` (and `&dark=1`) to the URL to fake the Telegram WebApp; Telegram's native buttons are not drawn in that mode.

### Scripts

| Script | What it does |
| --- | --- |
| `npm run gen:api` | regenerate `src/api/schema.ts` from `docs/backend/openapi.yaml` (commit the result) |
| `npm run check:api` | fail if `schema.ts` is stale |
| `npm run typecheck` / `lint` / `test` | tsc, ESLint, Vitest |
| `npm run e2e` | Playwright smoke tests against mock mode |
| `npm run e2e:real` | Playwright against a running Anora Runner (see below) |
| `npm run build` then `npm run check:size` | production build, then bundle budgets (initial JS ≤ 180 KB, CSS ≤ 30 KB gzipped) |

Build-time variables: `VITE_APP_VERSION` (git sha, set by the release workflow) and `VITE_DEBUG=1` (Telegram debug panel). There is no API URL: everything is same-origin.

### Against the real backend

Start Anora Runner's local stack (`deploy/docker-compose.dev.yml` in anora-runner: fake runner, no GitHub App, no Telegram); `npm run dev` then proxies `/api` and `/api/v1/ws` to `http://localhost:8080` (`ANORA_BACKEND_URL` in `.env.local` changes it). Log in with the password and a code for the TOTP secret you put in its `deploy/.env.dev`.

```sh
E2E_PASSWORD=… E2E_TOTP_SECRET=… npm run e2e:real
```

`e2e/real/` logs in through the UI, creates a project (and adds `octocat/Hello-World` through the API, since there is no GitHub App), creates tasks, answers the fake runner's question, queues a message during a run and checks the follow-up run; it fails on any unexpected HTTP error, console error or WebSocket error. With `E2E_TG_BOT_TOKEN`/`E2E_TG_USER_ID` (the backend's `TELEGRAM_BOT_TOKEN`/`TELEGRAM_ALLOWED_USER_ID`) it also embeds the app in a cross-site iframe with signed initData, as Telegram Web does, and checks it works through the bearer token and a WebSocket ticket without the cookie. `E2E_BACKEND` (default `http://localhost:8080`) and `E2E_PORT` (Vite, default 5173) change the URLs; `E2E_BASE_URL=https://…` runs the same tests against a deployed instance without a dev server (`E2E_INSECURE_TLS=1` for a local Caddy CA). Only point it at a deployment whose runner is fake: the tests create projects and tasks; the backend's `CORS_ORIGINS` must contain the Vite origin. Fake runs that wait for an answer hold one of the backend's concurrency slots, so the tests cancel what they leave open.

## Testing inside Telegram

1. Expose the dev server over HTTPS, e.g. `cloudflared tunnel --url http://localhost:5173` or `ngrok http 5173`. Add the tunnel host to `server.allowedHosts` in `vite.config.ts` if Vite rejects it.
2. In [@BotFather](https://t.me/BotFather): `/mybots` → your bot → *Bot Settings* → *Configure Mini App* → *Enable Mini App* and set the URL to the tunnel. Use `/newapp` instead for a named app link like `t.me/<bot>/<app>`.
3. The backend must accept your Telegram user id (`TELEGRAM_ALLOWED_USER_ID`). The *Access denied* screen shows the id it saw.
4. Deep links: `t.me/<bot>/<app>?startapp=task_123`, or `?task=123` on the app URL for `web_app` buttons.
5. Debugging:
   - **iOS**: in Telegram tap the *Settings* icon 10 times and turn on *Allow Web View Inspection*; connect the phone by USB and open Safari on a Mac → *Develop* → your device.
   - **Android**: enable USB debugging; in Telegram *Settings* scroll to the bottom, press and hold the version number twice, choose *Enable WebView Debug*; open `chrome://inspect/#devices` in desktop Chrome.
   - **Telegram Desktop (Windows/Linux, beta)**: *Settings → Advanced → Experimental settings → Enable webview inspection*, then right-click → *Inspect*.
   - **Telegram macOS (beta)**: click the *Settings* icon 5 times, enable *Debug Mini Apps*, then right-click → *Inspect Element*.
   - Build with `VITE_DEBUG=1` for an in-app panel showing platform, version, theme and safe-area insets.

## Deployment

The client runs on the same server and domain as Anora Runner, from Anora Runner's `deploy/docker-compose.deploy.yml` (service `anora-client`), behind Caddy, which sends `/api`, `/webhooks` and `/healthz` to the backend and everything else here. Server setup, `.env`, secrets and the first start are described once, in the [anora-runner README](../anora-runner/README.md#server-setup).

`release.yml` on push to `main`: builds `ghcr.io/andrewsl105/anora-client:<sha>` and `:latest`, then SSHes to the server and, under the shared `/srv/anora/.deploy.lock`, runs `docker compose -f docker-compose.deploy.yml pull anora-client` and `up -d --no-deps anora-client` in `/srv/anora`, so only this service restarts. It uses the same secrets as Anora Runner in its own `production` environment: `DEPLOY_HOST`, `DEPLOY_USER`, `DEPLOY_SSH_KEY`.

The image is Nginx serving `dist/` with an SPA fallback, `no-cache` for `index.html`, one-year immutable caching for hashed assets, gzip, and security headers (CSP with `connect-src 'self' wss://<host>` and `frame-ancestors` for the Telegram Web origins, no `X-Frame-Options`). See `nginx/`.
