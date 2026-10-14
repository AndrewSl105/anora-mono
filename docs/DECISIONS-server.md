# Decisions

Deviations from docs/SPEC.md, one line of reasoning each.

- 2026-10-14 — All eight phases built in one pass without stopping between phases: the owner explicitly waived the per-phase stop ("go for all phases").
- Runner `seq` continues across runs of a task (read from `.anora/events.jsonl`), so replay is a simple `seq > last persisted` filter per task.
- On exit 4 (limit exceeded) the runner commits work in progress before exiting, so a follow-up run continues from it instead of losing it.
- Runner `task_id` accepts a string or a number (tasks use bigserial ids); the Go side always sends a string.
- An `answer` that arrives before its `question` is buffered, so a whole session can be piped from a file.
- Runner image builds TypeScript in a `node:24-bookworm-slim` stage and runs on `mcr.microsoft.com/playwright:v1.64.1-noble`; the image's existing uid 1000 user is replaced by `runner`.
- Go module path: `github.com/andrewsl105/anora-runner`.
- Docker client is `github.com/moby/moby/client`: the spec's `github.com/docker/docker/client` path is deprecated in favour of the new moby module.
- GitHub App auth uses `github.com/bradleyfalzon/ghinstallation/v2` (the spec's `bradleyfitz` is a typo) and `google/go-github/v92` (latest major).
- `tasks` gets two extra columns, `paused_at` (for `SIDECAR_IDLE_TTL`) and `cleaned_at` (workspace cleanup bookkeeping); `runs` gets `created_at`.
- A queued run is a `runs` row with `started_at IS NULL`. Create, resume, follow-up and retry all insert one and set the task to `queued`, so the scheduler has a single path.
- The API exposes `max_duration` as `max_duration_sec` (integer seconds) instead of a Postgres interval string.
- Session cookie value is `<id>.<HMAC-SHA256(SESSION_SECRET, id)>`, so forged cookies are rejected before a DB lookup.
- `COOKIE_INSECURE=true` (local development only) drops the cookie's `Secure` flag so login works over plain http.
- Login rate limiting is in memory (single process). Client IP comes from `X-Forwarded-For` only when the direct peer is a private address.
- `PATCH /projects/{id}`: omitted fields are unchanged; `""` or `0` resets an override to the server default.
- Retry re-runs the original prompt as a fresh session (no resume) on the same branch.
- Answer delivered by the Mini App or web is recorded as `answered_via: "web"`; the bot records `telegram`.
