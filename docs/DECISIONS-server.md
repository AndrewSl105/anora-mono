# Decisions

Deviations from docs/SPEC.md, one line of reasoning each.

- 2026-10-14 — All eight phases built in one pass without stopping between phases: the owner explicitly waived the per-phase stop ("go for all phases").
- Runner `seq` continues across runs of a task (read from `.anora/events.jsonl`), so replay is a simple `seq > last persisted` filter per task.
- On exit 4 (limit exceeded) the runner commits work in progress before exiting, so a follow-up run continues from it instead of losing it.
- Runner `task_id` accepts a string or a number (tasks use bigserial ids); the Go side always sends a string.
- An `answer` that arrives before its `question` is buffered, so a whole session can be piped from a file.
- Runner image builds TypeScript in a `node:24-bookworm-slim` stage and runs on `mcr.microsoft.com/playwright:v1.64.1-noble`; the image's existing uid 1000 user is replaced by `runner`.
- Go module path: `github.com/andrewsl105/anora-runner`.
- Runner (phase 5): permissions use `permissionMode: "acceptEdits"` plus `allowedTools` for the nine listed tools, so nothing ever prompts; `AskUserQuestion` is available via `tools` but not pre-approved, so it always reaches `canUseTool`, which turns it into `question` events and returns the answers as `updatedInput.answers`. The SDK warns on stderr that `canUseTool` is shadowed for the pre-approved tools; that is intended, because the policy runs in a `PreToolUse` hook, which fires for every call regardless of approval.
- Runner policy also denies environment dumps (`env`, `printenv`, `export -p`, `set`, …) and any Bash command mentioning `CLAUDE_CODE_OAUTH_TOKEN`/`ANTHROPIC_API_KEY`, because Bash inherits the auth env. Residual risk: a program can still read its own env (e.g. `node -e 'process.env'`); the backend's secrets scan before push is the backstop.
- Runner policy recognises git/docker only in command position (after env assignments and wrappers such as sudo/xargs/timeout, and inside `bash -c`/`eval`), so `grep docker` or `grep "git push"` stay allowed.
- Runner relies on the SDK's `maxBudgetUsd` for the budget limit instead of comparing reported cost, because the reported total of a resumed session includes earlier runs.
- Runner `settingSources: ["project"]` loads the repo's `CLAUDE.md` and `.claude/settings.json`; user/local settings are not loaded.
- The Claude Code binary comes from the SDK's per-platform optional dependency (`@anthropic-ai/claude-agent-sdk-linux-<arch>`), installed by `npm ci` in the build stage for the target platform; it runs on the Playwright noble image (verified `2.1.299` on arm64).
