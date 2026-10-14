# Decisions and deviations from SPEC.md

One line each. "Backend ask" marks something Anora Runner should add.

## Backend contract gaps

- **Timeline paging is forward-only.** `/tasks/{id}/events` only takes `after_id`, so "latest 200 first, older on scroll-up" is impossible. The client loads all pages forward (200 per page) once, then fetches incrementally from the last id; the list is always virtualised. *Backend ask:* `before_id` (or `order=desc`).
- **No turns on Task.** Turns come from the latest `usage` event in the timeline, so they show only once the timeline is loaded. *Backend ask:* `turns` on Task.
- **No "latest activity" on Task, and `scope: "tasks"` sends no events.** Task cards show `status_reason`, CI status or the branch instead of "Editing src/…". *Backend ask:* a `last_activity` summary on Task.
- **No active-task count on Project.** Projects screen counts tasks from the Active list client-side (first 50 per page loaded).
- **Only `latest_run` in the API.** The Info tab's runs list is rebuilt from `run_started` / `run_finished` / `usage` events; per-run cost = session total minus the previous run's total (PROTOCOL.md). *Backend ask:* `GET /tasks/{id}/runs`.
- **Repositories can't be edited.** There is no PATCH for a repository, so base branch and default flag are set only when adding; change them by removing and re-adding. *Backend ask:* `PATCH /projects/{id}/repositories/{repoId}`.
- **Event payloads are untyped** (`payload: object` in OpenAPI). `src/api/events.ts` hand-writes the payload union from PROTOCOL.md and API.md — the one exception to "never hand-write API types". *Backend ask:* typed payload schemas.
- **No OpenAPI examples.** MSW mocks (`src/mocks/`) use hand-written fixtures typed from `schema.ts` instead of generated examples.
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
