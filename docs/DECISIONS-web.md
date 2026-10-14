# Decisions and deviations from SPEC.md

One line each. "Backend ask" marks something Anora Runner should add.

## Backend contract gaps

- ~~Timeline paging is forward-only~~ — resolved with `before_id`: a timeline opens at its end (`before_id=0`, 200 newest, reversed) and older pages load on scroll-up or with "Load earlier activity"; prepending shifts the scroll position by the added height so rows on screen stay put. Reconnect catch-up stays forward (`after_id` = newest cached id), so the cache never has gaps.
- ~~No turns on Task~~ — resolved: the header shows `Task.turns`.
- ~~No "latest activity" on Task~~ — resolved: cards and the "Claude is working" line show `Task.last_activity` (CI status instead for `pr_open`), and the card time is `last_activity_at`.
- **No active-task count on Project.** Projects screen counts tasks from the Active list client-side (first 50 per page loaded).
- ~~Only `latest_run` in the API~~ — resolved: the Info tab reads `GET /tasks/{id}/runs` (with the run's own `cost_usd` and `turns`), refetched on `run_started`/`run_finished` pushes and status changes.
- ~~Repositories can't be edited~~ — resolved: base branch and default flag are edited in place (`PATCH …/repositories/{repoId}`). The default switch is disabled on the current default, because un-defaulting it is `409`; make another repo the default instead.
- ~~Event payloads are untyped~~ — resolved: OpenAPI `Event` is now a discriminated `oneOf`; `src/api/events.ts` only derives `EventOf<K>`/`PayloadOf<K>` from the generated union, and `buildTimeline` narrows with `switch (event.type)`.
- **One cast at the events response boundary.** openapi-fetch 0.17 (`openapi-typescript-helpers` 0.1.0, both latest) drops properties typed exactly `null` in its `Readable<T>` response transform (`NonNullable<null>` is `never`), so backend events lose `seq: null`; `asEvents()` in `hooks.ts` restores the generated `Event` type. The contract stays as is.
- **No OpenAPI examples.** MSW mocks (`src/mocks/`) use hand-written fixtures typed from `schema.ts` instead of generated examples. The mock db maintains `turns`, `last_activity` and per-run cost like the backend, serves `before_id`, `/runs` and repository PATCH, and seeds a 500-event task to exercise paging.
- **`Retry-After` on 429 is undocumented.** Used when present; otherwise the message uses the 15-minute window from API.md.
- **`X-Requested-With` is a required header parameter in OpenAPI**, so generated types demand it per call. Calls pass the shared `CSRF` constant and a middleware also sets it on every non-GET.

## Client choices

- **TypeScript 6.0, not 7.** typescript-eslint supports `<6.1`; openapi-typescript and msw peer ranges are overridden in package.json.
- **React Router** (not TanStack Router): simpler data-less routes; typed params are only `/tasks/:id` and `/projects/:id`.
- **Official `telegram-web-app.js` + `@types/telegram-web-app`**, wrapped in `src/telegram/`, not `@telegram-apps/sdk-react`: the spec's CSP already allows `https://telegram.org`, and it avoids a large SDK in the bundle.
- **One small hand-written CSS file** (`src/routes/tasks/diff.css`) overrides react-diff-view so long lines scroll instead of wrapping on phones.
- **CSP needs `style-src 'unsafe-inline'`**: React style attributes, Radix positioning, the virtualiser and sonner all set inline styles. Scripts stay `'self'` + telegram.org.
- **No brotli.** `nginx:alpine` has no brotli module; gzip only.
- **Failed filter includes `cancelled`**, since there is no separate tab for it.
- **401 handling lives in the custom `fetch`** passed to openapi-fetch (one shared re-auth for concurrent 401s, one retry), and error-to-`ApiError` conversion in `call()` rather than a response middleware.
- **Mock mode** (`VITE_MOCK=1`) uses MSW in the browser for REST and MSW's WebSocket interception for `/api/v1/ws`; `?tg=1` (and `&dark=1`) fakes the Telegram WebApp. Mock code is excluded from production builds.
- **Phases 1–5 were built and verified together** (one commit) because the user asked to run all phases without stopping; Phase 6 is a separate commit.

## Integration with the real backend

- **Messages while a run is active are queued, not injected** (backend change, see its DECISIONS). `composerMode` is `queue` in `queued`/`preparing`/`running`/`waiting_input`/`pushing` and `followup` in `paused`/`pr_open`/`completed`; the composer says the text starts the next run when this one ends. The optimistic bubble uses `delivery: "queued"` and keeps a "Queued" note until a `status: queued` event (the drained follow-up) arrives; `message_dropped` turns it into "Not sent: the task failed/was cancelled first". `Task.queued_message` is shown above the composer. Legacy `delivery: "injected"` bubbles say the text may not have reached Claude.
- **Interrupt ends the run.** The action is labelled "Stop and finish run"; after it is sent (an `interrupt` event since the last `run_started`) the working line says the run is ending and the action is hidden until the run finishes. A queued message then starts the next run, so "queue a message, then stop" redirects Claude.
- **Telegram mode authenticates with a bearer token, not only the cookie.** Telegram Web K/A (`web.telegram.org`, `webk.`/`weba.telegram.org`) embed the Mini App in a cross-site iframe, where the `SameSite=Lax` cookie is neither sent nor stored; native Telegram apps load it top-level. The `token` from `POST /auth/telegram` lives in a module variable in `api/client.ts` (never in storage; a reload logs in again with initData) and goes out as `Authorization: Bearer` on every call; a 401 re-auth retries with the new token. Browser mode stays cookie-only.
- **WebSocket tickets.** With a bearer token, every (re)connect first calls `POST /api/v1/ws/ticket` and opens `/api/v1/ws?ticket=…` (single use, 30 s); `RealtimeClient` accepts an async `url()` and ignores a ticket that arrives after `stop()`/suspend. A failed ticket request backs off like a dropped socket.
- **CSP checked against the real deployment.** API and WebSocket are same-origin behind Caddy, so `connect-src 'self' wss://$host` (nginx fills in the request host; the old bare `wss:` allowed any host). `frame-ancestors` lists the Telegram Web origins that embed Mini Apps (`web.telegram.org`, `webk.telegram.org`, `weba.telegram.org`, checked against the tweb/telegram-tt sources) instead of `*.telegram.org`; native apps load the page top-level, so they need no entry. `img-src 'self' data:` stays: Claude's markdown cannot pull remote images.
- **Playwright against the real backend** (`npm run e2e:real`, `E2E_BACKEND`): `e2e/real/` drives login (TOTP computed from `E2E_TOTP_SECRET`), project and task creation, the fake runner's question, a queued message with its follow-up run and deep links, and fails on any unexpected 4xx/5xx, console error or WebSocket error. The repository is added through the API because the dev backend has no GitHub App. Wrong credentials stay mock-only: the backend's 5-failures-per-15-minutes limit would lock repeated runs out. The Telegram Web case is reproduced with a real loopback page (`127.0.0.1`) framing the app (`localhost`): Chromium's Local Network Access checks block a routed, address-less parent from framing localhost.
- **Deploy**: the service, Caddy and the routing live in anora-runner's `docker-compose.deploy.yml`/`Caddyfile` (one server setup, documented there). `release.yml` restarts only `anora-client` (`up -d --no-deps`) under the shared `/srv/anora/.deploy.lock`, and uses `set -e` because appleboy/ssh-action v1.2.5 dropped `script_stop`.

