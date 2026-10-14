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
| `npm run build` then `npm run check:size` | production build, then bundle budgets (initial JS ≤ 180 KB, CSS ≤ 30 KB gzipped) |

Build-time variables: `VITE_APP_VERSION` (git sha, set by the release workflow) and `VITE_DEBUG=1` (Telegram debug panel). There is no API URL: everything is same-origin.

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

`release.yml` on push to `main`: builds `ghcr.io/andrewsl105/anora-client:<sha>` and `:latest`, then SSHes to the server and runs `docker compose -f docker-compose.deploy.yml pull anora-client && … up -d anora-client` in `/srv/anora`. It uses the same secrets as Anora Runner: `DEPLOY_HOST`, `DEPLOY_USER`, `DEPLOY_SSH_KEY` (environment `production`).

**One-time server changes (not done by this repo):**

1. Add the service to `/srv/anora/docker-compose.deploy.yml` (and to `deploy/docker-compose.deploy.yml` in anora-runner):

   ```yaml
     anora-client:
       image: ghcr.io/andrewsl105/anora-client:${CLIENT_TAG:-latest}
       container_name: anora-client
       restart: unless-stopped
       networks: [proxy]
   ```

2. Reverse proxy on the domain: `/api/*` (with WebSocket upgrade for `/api/v1/ws`), `/webhooks/*` and `/healthz` → `anora:8080`; everything else → `anora-client:80`.

The image is Nginx serving `dist/` with an SPA fallback, `no-cache` for `index.html`, one-year immutable caching for hashed assets, gzip, and security headers (CSP with `frame-ancestors` for Telegram Web, no `X-Frame-Options`). See `nginx/`.
