# Decisions

Deviations from docs/SPEC.md, one line of reasoning each.

- 2026-10-14 — All eight phases built in one pass without stopping between phases: the owner explicitly waived the per-phase stop ("go for all phases").
- Runner `seq` continues across runs of a task (read from `.anora/events.jsonl`), so replay is a simple `seq > last persisted` filter per task.
- On exit 4 (limit exceeded) the runner commits work in progress before exiting, so a follow-up run continues from it instead of losing it.
- Runner `task_id` accepts a string or a number (tasks use bigserial ids); the Go side always sends a string.
- An `answer` that arrives before its `question` is buffered, so a whole session can be piped from a file.
- Runner image builds TypeScript in a `node:24-bookworm-slim` stage and runs on `mcr.microsoft.com/playwright:v1.64.1-noble`; the image's existing uid 1000 user is replaced by `runner`.
- Go module path: `github.com/andrewsl105/anora-runner`.
