# Anora Client — build prompt for Claude Code

2026-10-14 · Andrii Sllipchenko

## 1. Context and goal

Build **Anora Client**: the web frontend for Anora Runner, used both as a **Telegram Mini App** (primary, on a phone) and as a normal website in a desktop browser. It is a single-page app served as static files; all data and actions go through the Anora Runner REST API and WebSocket.

**How to use this document.** Put it in the new repo `anora-client` as `docs/SPEC.md`, copy `docs/API.md` and `docs/openapi.yaml` from the `anora-runner` repo into `docs/backend/`, and tell Claude Code: "Read docs/SPEC.md and implement Anora Client phase by phase as described in section 9. Stop after each phase and report."

**What Anora Runner is.** A single-user Go service that runs Claude Code sessions in isolated containers against my GitHub repos and opens PRs. A task moves through `queued → preparing → running ⇄ waiting_input → (paused) → pushing → pr_open → completed`, or ends `failed` / `cancelled`. While it runs, it emits a stream of events (Claude's text, tool use, questions, cost, commits). The backend spec is the source of truth for every endpoint, field and state name; this client must not invent any.

**What I do with the client**

- Create a task: pick a project (and repo if it has several), write the prompt.
- Watch a task live: what Claude is doing right now, its messages, cost, turns.
- Answer Claude's questions and send follow-up messages.
- See the diff and open the PR on GitHub.
- Cancel, retry, interrupt.
- Manage projects and repositories.

**Users.** Exactly one: me. There is no sign-up, no user management, no roles.

## 2. Stack and libraries

React + TypeScript + Vite, styled with Tailwind and shadcn/ui so no hand-written CSS is needed. Check every library's current version and API before use; do not rely on memory.

| Need | Choice | Notes |
|---|---|---|
| Build | Vite, TypeScript strict | output is static files only |
| UI | React (latest stable) | function components, hooks |
| Components | shadcn/ui (Radix primitives) + Tailwind CSS | copy components into `src/components/ui` |
| Icons | `lucide-react` |  |
| Routing | TanStack Router or React Router | typed routes, `/tasks/:id` deep links |
| Server state | TanStack Query | caching, retries, optimistic updates |
| API types | `openapi-typescript` + `openapi-fetch` | generated from `docs/backend/openapi.yaml`; never hand-write API types |
| Forms | `react-hook-form` + `zod` |  |
| Telegram | official `telegram-web-app.js` script, or `@telegram-apps/sdk-react` | pick one after checking which is maintained; wrap it in `src/telegram/` |
| Markdown | `react-markdown` + `remark-gfm` | Claude's messages and summaries |
| Code and diff | `react-diff-view` or a light Shiki-based viewer | loaded lazily |
| Time | `date-fns` | relative times ("2 min ago") |
| Toasts | shadcn `sonner` |  |
| Tests | Vitest + Testing Library, Playwright for a few E2E flows |  |

No global state library: TanStack Query holds server state, React context holds session and Telegram state, component state handles the rest.

## 3. Two run modes and authentication

The same build runs in both modes; it detects the mode at startup. Inside Telegram the user never sees a login screen; in a browser they log in with a password and a one-time code.

**Mode detection.** Telegram mode = the Telegram WebApp object exists **and** its `initData` string is non-empty. Anything else is browser mode. Expose the result through a `useRuntime()` hook (`{ mode: 'telegram' | 'browser', tg?: WebApp }`) so components never touch `window.Telegram` directly.

**Telegram mode: automatic login**

1. On startup, call `ready()` and `expand()` on the WebApp, then `POST /api/v1/auth/telegram` with `{ init_data: <raw initData string> }`. Send the raw string exactly as Telegram gave it; the backend verifies its HMAC signature with the bot token.
2. The backend accepts only the Telegram user id configured in `TELEGRAM_ALLOWED_USER_ID` (ids are stable; usernames can change, so do not rely on them) and sets the session cookie.
3. On success go straight to the app. Show a full-screen splash (logo + spinner) while this runs, never a login form.
4. On `401`/`403` show a single "Access denied" screen with the Telegram user's name and id, so I can see what to whitelist. No password fallback inside Telegram.
5. If the session expires mid-use (`401` from any call), silently repeat step 1 once, then retry the failed request. `initData` older than the backend's limit (1 hour) fails; then ask the user to reopen the Mini App.

**Browser mode: password login**

1. `GET /api/v1/me`. `200` → app. `401` → login screen.
2. Login screen: password field + 6-digit code field (TOTP), one "Sign in" button, `POST /api/v1/auth/login` with `{ password, totp }`. The code field uses `inputmode="numeric"`, `autocomplete="one-time-code"`, auto-submits on the 6th digit.
3. Errors: wrong credentials → one generic message; `429` → "Too many attempts, try again in N minutes" using `Retry-After` when present.
4. Logout in the settings menu calls `POST /api/v1/auth/logout`.

**Both modes**

- Session is an `HttpOnly` cookie set by the backend; the client never stores tokens in `localStorage`. All requests use `credentials: 'include'` and send `X-Requested-With: anora` on state-changing calls (the backend requires it).
- One `AuthGate` component wraps the router and renders splash, login, denied, or the app.
- The client is served on the same domain as the API (`/` → client, `/api` → backend), so no CORS configuration is needed in production. For local development, Vite proxies `/api` and `/api/v1/ws` to a backend URL from `.env.local`.

## 4. Screens and UX

Six screens cover everything; the task screen is the heart of the app and gets most of the design effort. On a phone the app is a stack of full-screen views with a bottom tab bar; on desktop (≥ 1024 px) it becomes a sidebar + list + detail layout.

**Navigation.** Bottom tabs on mobile: Tasks, New (a prominent centre button), Projects, Settings. On desktop the same items sit in a left sidebar, and the task list stays visible next to the open task.

**1. Tasks (home)**

- Segmented filter: Active (default: `queued`, `preparing`, `running`, `waiting_input`, `paused`, `pushing`), PR open, Done, Failed. Optional project filter chip.
- Card per task: title, project, status badge, one-line latest activity ("Editing src/tables/export.ts"), relative time, cost. Tasks in `waiting_input` are pinned to the top with an accent badge "Needs answer".
- Live: cards update from the WebSocket `tasks` subscription without refetching. Pull-to-refresh on mobile.
- Empty state with a button to create the first task.

**2. New task**

- Project picker (last used pre-selected), repo picker only when the project has more than one repo, prompt textarea (auto-grows, keeps a draft per project in `localStorage` wrapped in try/catch), optional title (otherwise derived by the backend).
- In Telegram, the submit action is the Telegram MainButton ("Start task"); in the browser, a normal button. `Ctrl/Cmd+Enter` submits on desktop.
- On success navigate to the new task.

**3. Task detail**

- Header: title, status badge, project/repo, branch, cost and turns, PR link button when it exists, overflow menu (Cancel, Interrupt, Retry, Copy branch, Open on GitHub).
- Tabs: **Activity** (default), **Diff**, **Info**.
- Activity is a chat-like timeline built from events: Claude's text as message bubbles (markdown), tool use collapsed into compact rows ("Edit · src/tables/export.ts", expandable to show the summary), status changes as small dividers, commits and PR events as cards, errors in red. Auto-scroll to the newest event unless the user scrolled up; then show a "New activity" pill.
- A **question** renders as a highlighted card: question text, option buttons when `options` exist, and a text field. Answering disables the card and shows "Answered via web/Telegram". Only one open question at a time.
- Composer at the bottom: sends `POST …/message`. Its placeholder depends on state: "Message Claude…" while running, "Follow-up for a new run…" in `paused`/`pr_open`/`completed`, hidden in states where the backend returns `409`.
- Running indicator: a subtle animated "Claude is working" line with the latest tool summary.
- Diff tab: file list with +/- counts, then per-file unified diff (split view on desktop). Lazy-loaded.
- Info tab: prompt, timestamps, runs list (kind, duration, cost, exit), CI status.

**4. Projects**

- List with repo count and active task count. Detail/edit form: name, instructions (multi-line, appended to Claude's system prompt), services (Postgres, Redis toggles), env (key/value editor, non-secret only), model, limits (turns, budget USD, duration, memory, CPUs), repos (add from `GET /api/v1/github/repositories` picker, base branch, default flag, remove).

**5. Settings**

- Stats from `GET /api/v1/stats` (running, queued, cost today and this month), theme override in browser mode (system/light/dark), logout (browser mode only), app version and backend health.

**6. System screens**

- Splash, login, access denied, not found, offline banner ("Reconnecting…" while the WebSocket is down), and an error boundary with a "Reload" action.

**Copy.** English UI text, short and plain; keep all strings in one `src/i18n/en.ts` file so a Ukrainian translation can be added later.

## 5. Data layer: API client, caching, WebSocket

REST loads state; one WebSocket keeps it live; TanStack Query is the single cache both write into. Components never call `fetch` directly.

**API client (****`src/api/`****)**

- `npm run gen:api` runs `openapi-typescript` on `docs/backend/openapi.yaml` → `src/api/schema.ts`. Commit the generated file; CI fails if it is stale.
- One `openapi-fetch` client with `baseUrl: '/'`, `credentials: 'include'`, the `X-Requested-With: anora` header, and a middleware that turns `{error:{code,message}}` into a typed `ApiError` and triggers re-auth on `401` (section 3).
- One hook per use case in `src/api/hooks.ts`: `useTasks(filter)`, `useTask(id)`, `useTaskEvents(id)` (infinite, `after_id` cursor), `useCreateTask()`, `useAnswer()`, `useSendMessage()`, `useProjects()`, and so on. Query keys live in one `keys.ts`.
- Mutations: answer and message are optimistic (show immediately, roll back on error with a toast).

**WebSocket (****`src/realtime/`****)**

- One connection per app session to `/api/v1/ws` (`wss://` on https), opened after auth succeeds.
- Subscriptions are reference-counted: the task list subscribes to `{scope:"tasks"}`, an open task to `{task_id}`; unmount unsubscribes.
- Incoming `event` messages are appended to the `useTaskEvents(id)` cache (deduplicated by event `id`); `task` messages replace that task in the list and detail caches.
- Reconnect with exponential backoff (1 s → 30 s, jitter). After reconnect, re-subscribe and fetch missed events with `after_id` = last event id in cache, so nothing is lost. Show the offline banner while disconnected.
- When the Mini App goes to the background (`visibilitychange` hidden for more than 60 s), close the socket; reconnect and catch up on return.

**Performance of the timeline.** Tasks can produce thousands of events. Load the latest 200 first, page older ones on scroll-up, and virtualise the list (`@tanstack/react-virtual`) once it exceeds a few hundred rows.

## 6. Telegram Mini App integration

Inside Telegram the app must feel native: Telegram's colours, buttons, back gesture and haptics, no browser chrome. All Telegram calls go through `src/telegram/`; every feature checks the WebApp version (`isVersionAtLeast`) and degrades quietly on older clients.

**Startup.** `ready()` as soon as the first screen can render, `expand()`, then auth (section 3). Load the official script synchronously in `index.html` before the app bundle if you use it directly.

**Theme.** Map Telegram `themeParams` (bg, secondary bg, text, hint, link, button, button text, destructive text, section bg, accent) onto the shadcn/Tailwind CSS variables, so every component follows the user's Telegram theme. Follow `colorScheme` for dark/light. Re-apply on `themeChanged`. Set the header and background colours via `setHeaderColor` / `setBackgroundColor` to match the app.

**Viewport and safe areas.** Use Telegram's stable viewport height for full-height layouts (not `100vh`), update on `viewportChanged`, and pad for safe-area insets (device and Telegram content insets where the version provides them). The composer and bottom tab bar must never hide behind the keyboard or the home indicator.

**Native controls**

| Control | Use |
|---|---|
| BackButton | shown on every screen deeper than a tab root; navigates back |
| MainButton | primary action of the screen: "Start task" on New task, "Send answer" when a question card has focus; shows progress while the request runs |
| SecondaryButton | optional, e.g. "Cancel task" when supported |
| HapticFeedback | light impact on taps of primary actions, notification success/error on results, selection change on tab switches |
| Closing confirmation | enabled while the prompt or composer contains unsent text |
| Vertical swipes | disable swipe-to-close while the timeline is scrollable, if supported |
| Popups | `showConfirm` for Cancel task instead of an in-app dialog |

In browser mode the same actions render as normal shadcn buttons; implement this once through a `<PrimaryAction>` component that chooses MainButton or a button by mode.

**Deep links.** The bot's buttons open the Mini App on a specific task. Support both: `start_param` from `initDataUnsafe` in the form `task_<id>` (for `t.me/<bot>/<app>?startapp=task_123` links) and a `?task=<id>` query parameter on the app URL (for `web_app` inline buttons). After auth, navigate to `/tasks/<id>`.

**Testing in Telegram.** Document in the README how to register the Mini App with BotFather, point it at a dev tunnel, and debug on iOS, Android and Telegram Desktop. Add an in-app debug panel (only when `VITE_DEBUG=1`) showing platform, version, theme and insets.

## 7. Mobile, design and performance

Design mobile-first for a 360–430 px wide phone inside Telegram, then scale up to desktop. It should look like a calm, modern developer tool: functional first, but polished.

**Layout and touch**

- Touch targets at least 44 × 44 px; primary actions within thumb reach at the bottom of the screen.
- No horizontal page scroll at any width; long paths and code wrap or scroll inside their own block.
- Text inputs at 16 px or larger so iOS does not zoom on focus.
- Breakpoints: phone (< 640), tablet (640–1023, single column wider), desktop (≥ 1024, sidebar + list + detail).
- Keyboard: the composer stays above the on-screen keyboard; on desktop, `Ctrl/Cmd+Enter` sends, `Esc` closes dialogs, `/` focuses search on the task list.

**Visual design**

- shadcn/ui defaults as the base, one accent colour, neutral surfaces; in Telegram the accent and surfaces come from `themeParams`.
- Status colours used consistently everywhere: running = accent, waiting_input = amber, pr_open = violet, completed = green, failed = red, cancelled/paused = grey. Always paired with a label or icon, never colour alone.
- Monospace font for paths, branches, diffs; system font stack for everything else (no web font download).
- Subtle motion only: list item enter, tab transitions, the "working" indicator. Respect `prefers-reduced-motion`.
- Light and dark both fully supported in browser mode (system default, user override in Settings).

**Performance budgets** (measure in CI with a bundle analyser)

| Metric | Budget |
|---|---|
| Initial JS, gzipped | ≤ 180 KB |
| Initial CSS, gzipped | ≤ 30 KB |
| First screen visible after Mini App opens (mid-range Android, 4G) | ≤ 1.5 s |
| Task screen interaction (typing, scrolling) | 60 fps, no long tasks over 100 ms |

Code-split by route; lazy-load the diff viewer, markdown highlighting and the projects editor. Hashed asset file names with long cache headers; `index.html` not cached.

**Accessibility.** Semantic HTML, labelled inputs and icon buttons, visible focus rings, colour contrast AA in both themes.

## 8. Repo layout, Docker, deployment

The client ships as a small Nginx image with static files, deployed next to Anora Runner on the same server and domain, the same way as my other projects: GitHub Actions builds the image, pushes it to GHCR, and deploys over SSH.

**Repo layout**

```
anora-client/
├── src/
│   ├── api/            # generated schema, client, hooks, query keys
│   ├── realtime/       # WebSocket connection and subscriptions
│   ├── telegram/       # WebApp wrapper, theme mapping, native controls
│   ├── auth/           # AuthGate, login, denied, session context
│   ├── routes/         # tasks, task detail, new task, projects, settings
│   ├── components/     # app components; ui/ = shadcn components
│   ├── i18n/           # en.ts
│   └── lib/            # formatting, status colours, utils
├── docs/               # SPEC.md, backend/API.md, backend/openapi.yaml
├── nginx/default.conf
├── Dockerfile
└── .github/workflows/  # ci.yml, release.yml
```

**Dockerfile.** Multi-stage: `node` stage runs `npm ci && npm run build`; final stage `nginx:alpine` copies `dist/` and `nginx/default.conf`. Image `ghcr.io/andrewsl105/anora-client:<sha>` and `:latest`.

**Nginx config**

- SPA fallback: unknown paths serve `index.html`.
- `Cache-Control: no-cache` for `index.html`; one year, `immutable` for hashed assets.
- gzip and brotli (if available) for JS, CSS, SVG.
- Security headers. Do **not** send `X-Frame-Options: DENY`: Telegram Web and Telegram Desktop load Mini Apps in an iframe. Use `Content-Security-Policy` with `frame-ancestors 'self' https://web.telegram.org https://*.telegram.org` (verify the current Telegram domains), `script-src 'self' https://telegram.org`, `connect-src 'self' wss:`, plus `Referrer-Policy` and `X-Content-Type-Options`.

**Routing on the server.** The reverse proxy on the domain sends `/api/*`, `/webhooks/*` and `/healthz` to the `anora` container and everything else to `anora-client`, with WebSocket upgrade enabled for `/api/v1/ws`. Add the client service to the same compose file and `proxy` network as Anora Runner:

```yaml
  anora-client:
    image: ghcr.io/andrewsl105/anora-client:${CLIENT_TAG:-latest}
    restart: unless-stopped
    networks: [proxy]
```

**Workflows**

- `ci.yml` on pull requests: typecheck, lint, unit tests, check that `src/api/schema.ts` matches the OpenAPI file, build, bundle-size check against section 7.
- `release.yml` on push to `main`: build and push the image, SSH to the server, `docker compose pull anora-client && docker compose up -d anora-client`.

**Config.** Build-time variables only: `VITE_APP_VERSION` (git sha) and `VITE_DEBUG`. No API URL is needed in production because everything is same-origin.

## 9. Implementation phases and working rules

Build in six phases. Each phase ends with a working build, tests, and a short report; stop and wait for my go-ahead before the next one. Until Anora Runner is deployed, develop against a mock API (MSW handlers generated from the OpenAPI examples) so no phase is blocked by the backend.

1. **Foundation.** Vite + TS + Tailwind + shadcn setup, routing, generated API client, MSW mock mode (`VITE_MOCK=1`), `useRuntime()`, theme system, app shell with bottom tabs and desktop sidebar, error boundary.
2. **Auth.** AuthGate, Telegram auto-login, browser login with TOTP, denied screen, re-auth on `401`, logout. Test both modes (Telegram mode with a fake WebApp object in tests).
3. **Tasks.** Task list with filters, new task screen, task detail with Activity, Info, question card, composer, actions menu, optimistic answers and messages.
4. **Real-time.** WebSocket client, subscriptions, cache updates, reconnect and catch-up, offline banner, background handling.
5. **Telegram polish and the rest.** Theme mapping, MainButton/BackButton/haptics/closing confirmation, deep links, safe areas, Diff tab, Projects screens, Settings and stats.
6. **Ship.** Dockerfile, Nginx config with CSP, CI and release workflows, bundle budgets, README (local dev, mock mode, BotFather Mini App setup, deploy).

**Working rules**

- The backend contract in `docs/backend/` is the source of truth. If something the UI needs is missing or unclear, stop and tell me what the backend should add; do not invent endpoints, fields or states.
- Verify library and Telegram WebApp APIs against current docs and installed versions before coding.
- Mobile first: check every screen at 375 px inside a Telegram-like frame before desktop.
- Keep components small and typed; no `any`. Shared status labels, colours and icons live in one place (`src/lib/status.ts`).
- Tests: unit tests for auth flow, mode detection, deep-link parsing, event-to-timeline mapping and WebSocket catch-up; a Playwright smoke test for login, create task, answer question in mock mode.
- Record any deviation from this spec in `docs/DECISIONS.md` with one line of reasoning. Small, focused commits.
