# Runner protocol

Anora Runner (Go) and the runner (`runner/`, Node) talk over the runner container's attached stdin/stdout using JSON Lines: one JSON object per line, UTF-8, `\n`-terminated. There are no ports. Stderr is free-form diagnostics and is only logged.

Source of truth: `runner/src/protocol.ts`. Go mirror: `internal/runnermgr/protocol.go`.

## Lifecycle

1. The runner starts and emits `ready`.
2. The backend writes exactly one `run` command. The runner works in `/workspace` (a git checkout on the task branch).
3. While the session runs the backend may write `answer`, `message`, `interrupt` or `cancel`.
4. The runner ends with one of the exit codes below. Before exiting with `0` it commits (never pushes) and emits `committed` (only when the tree changed) and then `done`.

## Commands (stdin)

| type | fields | meaning |
|---|---|---|
| `run` | `task_id` (string or number), `prompt`, `append_system_prompt?`, `resume_session_id?`, `model?`, `limits{max_turns, max_budget_usd, max_duration_sec, question_timeout_sec}`, `commit{message, author_name, author_email}` | first line, starts the session |
| `answer` | `question_id`, `text` | reply to a pending question. An answer that arrives before its question is kept and used when the question is asked. |
| `message` | `text` | extra user message injected into the running session |
| `interrupt` | — | stop the current turn, keep the session |
| `cancel` | — | end the session, exit code `2` |

An invalid line produces an `error` event with `code: "bad_command"`; the runner keeps running. A second `run` is also `bad_command`.

## Events (stdout)

Every event has `type`, `ts` (RFC 3339, UTC, milliseconds) and `seq` (monotonic integer).

`seq` continues across runs of the same task: a new runner process reads `/workspace/.anora/events.jsonl` and continues after its last `seq`. The backend can therefore replay the file by selecting `seq > last persisted seq`.

| type | fields |
|---|---|
| `ready` | `runner_version` |
| `started` | `session_id`, `model` |
| `assistant_text` | `text` |
| `tool_use` | `tool`, `summary` (short, human-readable, e.g. "Edit src/tables/export.ts") |
| `tool_result` | `tool`, `ok`, `summary?` |
| `question` | `question_id`, `text`, `options?` (string array) |
| `usage` | `cost_usd_total`, `input_tokens`, `output_tokens`, `turns` |
| `committed` | `sha`, `files_changed`, `insertions`, `deletions` |
| `done` | `session_id`, `summary`, `no_changes`, `cost_usd_total` |
| `paused` | `session_id`, `reason` (`question_timeout`), `question_id` |
| `error` | `code`, `message`, `session_id?` |
| `heartbeat` | — (every 15 s) |

Error codes: `no_auth`, `bad_command`, `commit_failed`, `limit_exceeded`, `sdk_error`, `internal`, `not_implemented`.

## Exit codes

| code | meaning | last events |
|---|---|---|
| `0` | done | `committed?`, `done` |
| `1` | error | `error` |
| `2` | cancelled (`cancel` command or SIGTERM) | — |
| `3` | paused (question not answered within `question_timeout_sec`) | `paused` |
| `4` | limit exceeded (turns, budget or duration) | `committed?`, `error{code:"limit_exceeded"}` |

On exit `4` the runner still commits work in progress so a follow-up run can continue from it.

## Event log

Every event is also appended to `/workspace/.anora/events.jsonl`. The runner adds `.anora/` to `.git/info/exclude` so the log is never committed.

## Environment

| variable | meaning |
|---|---|
| `CLAUDE_CODE_OAUTH_TOKEN` / `ANTHROPIC_API_KEY` | Claude auth; the OAuth token wins. Missing both → `error{code:"no_auth"}`, exit `1`. |
| `ANORA_RUNNER_FAKE=1` | scripted session, no Claude: `started`, tool events, one question (`q1`, options `hello`/`hi`), waits for the answer, writes `ANORA_FAKE.md`, commits, `done`. |
| `ANORA_WORKSPACE` | workspace path, default `/workspace` (tests only). |

## Example

```
→ {"type":"run","task_id":1,"prompt":"Say hello","limits":{"max_turns":10,"max_budget_usd":1,"max_duration_sec":60,"question_timeout_sec":30},"commit":{"message":"agent: say hello","author_name":"anora[bot]","author_email":"anora@example.com"}}
← {"type":"ready","runner_version":"0.1.0","ts":"…","seq":1}
← {"type":"started","session_id":"…","model":"…","ts":"…","seq":2}
← {"type":"question","question_id":"q1","text":"Which greeting should I write?","options":["hello","hi"],"ts":"…","seq":8}
→ {"type":"answer","question_id":"q1","text":"hello"}
← {"type":"committed","sha":"…","files_changed":1,"insertions":3,"deletions":0,"ts":"…","seq":12}
← {"type":"done","session_id":"…","summary":"…","no_changes":false,"cost_usd_total":0.01,"ts":"…","seq":13}
```
