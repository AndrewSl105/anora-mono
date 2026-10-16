# Design: multi-provider agents (Claude, Codex, Gemini)

Status: **phase 1 proposal, awaiting approval.** Nothing in this file is implemented. Once approved, the spec diffs in section 3 are applied to the real documents in the same commits as the code they describe (phase 2 order in section 7), and this file is deleted or kept as a historical note (open question Q12).

Research date: 2026-10-16. Versions checked: `@openai/codex-sdk` 0.163.0 (pins `@openai/codex` 0.163.0), `@google/gemini-cli` 0.64.0, `@anthropic-ai/claude-agent-sdk` 0.3.299 (installed). Facts marked **(pkg)** were read from the published package (type definitions or bundled source). Facts marked **(docs)** come from the linked pages. Facts marked **(verify)** are things I could not confirm and that phase 2 has to check against the installed binary before code depends on them.

---

## 1. Research

### 1.1 OpenAI Codex: `@openai/codex-sdk`

The SDK is a thin wrapper. It spawns the bundled `codex` binary as `codex exec --experimental-json …` and parses its JSONL stdout **(pkg, `dist/index.js`)**.

| Topic | Finding |
|---|---|
| Start a run | `new Codex(opts).startThread(threadOptions)` then `thread.runStreamed(input, {signal})` **(pkg)**. `input` is a string or `[{type:"text",text}, {type:"local_image",path}]`. |
| Stream events | `runStreamed` returns `{events: AsyncGenerator<ThreadEvent>}`. Event types: `thread.started{thread_id}`, `turn.started`, `turn.completed{usage}`, `turn.failed{error}`, `item.started/updated/completed{item}`, `error{message}`. Item types: `agent_message`, `reasoning`, `command_execution{command, aggregated_output, exit_code, status}`, `file_change{changes[{path,kind}], status}`, `mcp_tool_call{server, tool, arguments, result, error, status}`, `web_search{query}`, `todo_list`, `error` **(pkg)**. |
| Resume | `codex.resumeThread(thread_id, threadOptions)`; the id is `thread_id` from `thread.started` (`thread.id` once known). The CLI equivalent is `codex exec resume <SESSION_ID>` **(pkg, docs)**. |
| Model | `ThreadOptions.model` → `--model` **(pkg)**. |
| Effort | `ThreadOptions.modelReasoningEffort`: `minimal, low, medium, high, xhigh, max, ultra, persistent` **(pkg)**. The docs say the valid levels depend on the model. |
| Working dir | `ThreadOptions.workingDirectory` → `--cd`. `skipGitRepoCheck`. `additionalDirectories` → `--add-dir` (for `/attachments`) **(pkg)**. |
| MCP servers | Config `mcp_servers.<id>.{command, args, env, env_vars, startup_timeout_sec, tool_timeout_sec (default 60), enabled_tools, disabled_tools, default_tools_approval_mode, tools.<tool>.approval_mode}` **(docs)**. The SDK passes them through `CodexOptions.config` (flattened to `--config key=value`) **(pkg)**. |
| Base URL | `CodexOptions.baseUrl` → `--config openai_base_url=…` **(pkg)**. A project-local `.codex/config.toml` cannot override it **(docs)**. |
| Auth | `CodexOptions.apiKey` → env `CODEX_API_KEY` for the child **(pkg)**. `CODEX_API_KEY` is the documented variable for `codex exec` **(docs)**. The docs do not say how `OPENAI_API_KEY` is prioritised against it, so we pass only `CODEX_API_KEY`. |
| Sandbox / approval | `sandboxMode`: `read-only` (exec default), `workspace-write`, `danger-full-access`. `approvalPolicy`: `never`, `on-request`, `on-failure`, `untrusted` in the SDK types; the docs say `untrusted` is unsupported and `on-failure` is deprecated. `networkAccessEnabled` → `sandbox_workspace_write.network_access` **(pkg, docs)**. |
| Policy hooks | `PreToolUse`/`PostToolUse` hooks cover shell (matched as `Bash`), `apply_patch`, MCP tools and other local tools, but not hosted tools such as web search. Deny with `{"hookSpecificOutput":{"hookEventName":"PreToolUse","permissionDecision":"deny","permissionDecisionReason":…}}`. Hooks are on by default, but **non-managed hooks need persisted trust** (`/hooks`) or `--dangerously-bypass-hook-trust`, and the SDK does not expose that flag **(docs; verify which hook source is trusted in `exec`)**. |
| Env exposure | `shell_environment_policy` (`inherit`, `filters`, `set`, …) controls what env the shell tool sees **(docs)**. The default excludes are said to drop `*KEY*`/`*TOKEN*` variables **(verify)**. |
| System prompt | `developer_instructions` config key adds developer instructions **(docs)**. Repo instructions come from `AGENTS.md`; `project_doc_fallback_filenames` can add `CLAUDE.md` **(docs)**. |
| Turn / budget limits | **No max-turns or budget option** in `ThreadOptions` or the documented config. The adapter has to enforce both. |
| Usage / cost | `turn.completed.usage = {input_tokens, cached_input_tokens, cache_write_input_tokens, output_tokens, reasoning_output_tokens}` **(pkg)**. **No USD cost.** |
| Cancel / interrupt | `TurnOptions.signal` (AbortSignal) kills the child **(pkg)**. There is no soft "interrupt the turn but keep running". The rollout up to that point stays resumable **(verify)**. |
| Mid-run input | None: one `runStreamed` per turn. That is fine, because Anora already runs one prompt per run. |
| Ask-the-user | `codex exec` has no built-in tool that asks the user a question. Questions are only possible through our MCP tool. |
| Local state | `$CODEX_HOME` (default `~/.codex`): config, `auth.json`, session rollouts, `history.jsonl`, `log/`, `sqlite_home` **(docs; rollout path layout: verify)**. `--ephemeral` disables rollouts, which would also break resume. |
| Binary | `@openai/codex` uses per-platform optional deps; `@openai/codex-linux-x64` is **430 MB unpacked** (pkg metadata). |

Links: [npm @openai/codex-sdk](https://www.npmjs.com/package/@openai/codex-sdk), [SDK source](https://github.com/openai/codex/tree/main/sdk/typescript), [non-interactive mode](https://learn.chatgpt.com/docs/non-interactive-mode), [config reference](https://learn.chatgpt.com/docs/config-file/config-reference), [hooks](https://learn.chatgpt.com/docs/hooks). (The developers.openai.com/codex pages now redirect to learn.chatgpt.com.)

### 1.2 Google Gemini CLI: headless `--output-format stream-json`

There is no Node SDK for driving a Gemini CLI session. The adapter spawns `gemini -p <prompt> --output-format stream-json` and reads JSONL.

**Status warning.** The Gemini CLI docs say the CLI "was replaced by Antigravity CLI on June 18th, 2026, for unpaid tier and Google One users". Press reports say paid Gemini API-key and Vertex users keep Gemini CLI ([The New Stack](https://thenewstack.io/gemini-cli-antigravity-replacement/), [letsdatascience](https://letsdatascience.com/news/google-replaces-gemini-cli-with-antigravity-cli-574b2056)). The npm package is still released (0.64.0, published 2026-10-16). The docs also say the CLI is "not optimized for Gemini 4 Argon". See Q1.

| Topic | Finding |
|---|---|
| Start a run | `gemini -p "<prompt>" --output-format stream-json` (choices `text`, `json`, `stream-json`) **(pkg)**. A non-TTY also triggers headless mode **(docs)**. |
| Stream events | JSONL with `type`: `init{session_id, model}`, `message{role, content, delta?}`, `tool_use{tool_name, tool_id, parameters}`, `tool_result{tool_id, status: success\|error, output}`, `error`, `result{status, error?, stats}` **(pkg, docs)**. |
| Exit codes | `0` success, `1` error or API failure, `42` input error, `53` turn limit exceeded **(docs)**. |
| Resume | `--resume <id>`: `latest`, a 1-based index, or the session UUID (it matches `session.id`) **(pkg)**. `--session-id <uuid>` starts a new session with an id we choose **(pkg)**. Sessions are stored per project (keyed by the working directory) under the Gemini home `tmp/<project>/chats` **(pkg, verify layout)**. `/workspace` is constant, so resume works. |
| Model | `--model` / `-m` **(pkg)**. |
| Effort | **No CLI flag** found. Thinking budgets can only be set through model config aliases in settings **(verify; v1: unsupported)**. |
| Working dir | The process `cwd`. `--include-directories` adds extra roots (for `/attachments`) **(pkg)**. |
| MCP servers | `settings.json` → `mcpServers.<name>.{command, args, env, cwd, timeout (ms), trust, includeTools, excludeTools}`, plus `--allowed-mcp-server-names` **(pkg; `trust` skips confirmation)**. |
| Base URL | Env `GOOGLE_GEMINI_BASE_URL` (Vertex: `GOOGLE_VERTEX_BASE_URL`) **(pkg)**. |
| Auth | Env `GEMINI_API_KEY` **(pkg, docs)**. |
| Approval / sandbox | `--approval-mode default\|auto_edit\|yolo\|plan`, `--yolo`, `--sandbox` (Docker/Podman/Seatbelt; not usable inside our runner, which has no Docker), `--skip-trust` / `GEMINI_CLI_TRUST_WORKSPACE=true` for folder trust, `--policy <file>` for policy-engine rules **(pkg)**. In headless mode, tools that need confirmation cannot be confirmed, so we use `yolo` plus our own hook. |
| Policy hooks | `settings.json` `hooks.BeforeTool[{matcher (regex), hooks:[{type:"command", command, timeout}]}]`. Block with exit 0 + `{"decision":"deny"}` on stdout, or exit 2 with the reason on stderr **(docs)**. Whether hooks run in headless mode is not documented **(verify)**. System settings file via `GEMINI_CLI_SYSTEM_SETTINGS_PATH` **(pkg)**. |
| System prompt | `GEMINI_SYSTEM_MD` **replaces** the whole system prompt, so it is unsuitable. To append, write the extra prompt into the global context file `<gemini home>/GEMINI.md`. `context.fileName` can also load `AGENTS.md`/`CLAUDE.md` from the repo **(pkg)**. |
| Turn / budget limits | Settings `model.maxSessionTurns` (exit 53) **(pkg: key exists; verify path)**. **No budget option.** |
| Usage / cost | `result.stats = {total_tokens, input_tokens, output_tokens, cached, input, duration_ms, tool_calls, models{<model>:{…}}}` **(pkg)**. **No USD cost.** The counters are per process (telemetry service), so not cumulative across resumes **(verify)**. |
| Cancel / interrupt | Kill the process (SIGINT/SIGTERM). There is no soft interrupt in headless mode. |
| Local state | `$GEMINI_CLI_HOME` (else `~`) `/.gemini/`: `settings.json`, `tmp/<project>/chats`, … **(pkg)**. |
| Package | `@google/gemini-cli` is **99 MB unpacked**, Node ≥ 20 **(pkg)**. |

Links: [npm @google/gemini-cli](https://www.npmjs.com/package/@google/gemini-cli), [headless mode](https://geminicli.com/docs/cli/headless/), [hooks](https://geminicli.com/docs/hooks/), [repository](https://github.com/google-gemini/gemini-cli).

### 1.3 Claude (existing), for comparison

The installed `@anthropic-ai/claude-agent-sdk` 0.3.299 has `Options.effort` (`low|medium|high|xhigh|max` or a number), `mcpServers`, and `createSdkMcpServer` (an in-process MCP server) **(pkg)**. It reports `total_cost_usd`. The base URL is `ANTHROPIC_BASE_URL`. State lives in `~/.claude` (plus `~/.claude.json`).

### 1.4 Capability matrix

| | Claude | Codex | Gemini |
|---|---|---|---|
| Resume id | `session_id` | `thread_id` | `session_id` (UUID) |
| Model / effort | yes / yes | yes / yes | yes / **no** |
| Append system prompt | `systemPrompt.append` | `developer_instructions` | global `GEMINI.md` (workaround) |
| Repo instructions file | `CLAUDE.md` | `AGENTS.md` (+ fallback names) | `GEMINI.md` (+ `context.fileName`) |
| MCP (stdio) | yes (also in-process) | yes | yes |
| Pre-tool policy hook | yes (in-process) | yes, **trust caveat** | yes, **headless unverified** |
| Max turns | SDK | **adapter** | `model.maxSessionTurns` |
| Budget USD | SDK `maxBudgetUsd` | **adapter, needs price table** | **adapter, needs price table** |
| Cost in USD | yes | **no** (tokens) | **no** (tokens) |
| Soft interrupt | yes | **no** (kill) | **no** (kill) |
| Base URL override | `ANTHROPIC_BASE_URL` | `openai_base_url` | `GOOGLE_GEMINI_BASE_URL` |
| Key env | `CLAUDE_CODE_OAUTH_TOKEN` / `ANTHROPIC_API_KEY` | `CODEX_API_KEY` | `GEMINI_API_KEY` |
| State dir | `~/.claude` | `$CODEX_HOME` | `$GEMINI_CLI_HOME/.gemini` |

---

## 2. Design

### 2.1 Data model (migration `00007_agent_profiles.sql`)

```sql
CREATE TABLE agent_profiles (
  id          bigserial PRIMARY KEY,
  name        text NOT NULL UNIQUE,
  provider    text NOT NULL CHECK (provider IN ('claude','codex','gemini')),
  model       text,                         -- NULL = provider default
  options     jsonb NOT NULL DEFAULT '{}',  -- validated per provider (2.3)
  is_default  boolean NOT NULL DEFAULT false,
  created_at  timestamptz NOT NULL DEFAULT now(),
  updated_at  timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX agent_profiles_one_default ON agent_profiles (is_default) WHERE is_default;

ALTER TABLE projects ADD COLUMN default_agent_profile_id bigint REFERENCES agent_profiles(id);
ALTER TABLE tasks
  ADD COLUMN agent_profile_id bigint REFERENCES agent_profiles(id) ON DELETE SET NULL,
  ADD COLUMN provider      text,        -- snapshot, NOT NULL after backfill
  ADD COLUMN model         text,        -- snapshot (NULL = provider default)
  ADD COLUMN agent_options jsonb,       -- snapshot of options (see 2.2)
  ADD COLUMN parent_task_id bigint REFERENCES tasks(id) ON DELETE SET NULL,
  ADD COLUMN input_tokens  bigint NOT NULL DEFAULT 0,
  ADD COLUMN output_tokens bigint NOT NULL DEFAULT 0;
ALTER TABLE runs
  ADD COLUMN input_tokens  bigint NOT NULL DEFAULT 0,
  ADD COLUMN output_tokens bigint NOT NULL DEFAULT 0,
  ADD COLUMN cost_known    boolean NOT NULL DEFAULT true;
```

- The global default is `agent_profiles.is_default` (a partial unique index allows at most one), not a settings table.
- **Migration and bootstrap.** SQL cannot read env, so the SQL migration inserts the profile `Claude` (`provider='claude'`, `model` = the most common non-null `projects.model`, else NULL, `is_default=true`). It then sets `tasks.provider='claude'`, `tasks.model` (from the project override) and `tasks.agent_profile_id`, and makes `tasks.provider` `NOT NULL`. On startup, Go fills the default profile's `model` from `DEFAULT_MODEL` when it is NULL and the env is set (once; a marker in `options`). Projects with their own `model` override get their own profile `Claude (<model>)` as project default. `projects.model` is then removed from the API (Q4).
- Retention and audit: deleting a profile is `409 in_use` while it is a project default or the global default. Tasks keep working through their snapshot (`ON DELETE SET NULL`).

### 2.2 Resolution and immutability

- At creation: `profile = req.agent_profile_id ?? project.default_agent_profile_id ?? global default`. Then `tasks.provider/model/agent_options` = the profile's values, **written once in the creating transaction and never updated** (no code path writes them later; a test asserts this across follow-up, resume, retry and a profile edit).
- Limits keep their current precedence and source: `max_turns = project.max_turns ?? profile.options.max_turns ?? DEFAULT_MAX_TURNS`; budget and duration stay project/server settings (Q5).
- Follow-ups, resumes and retries read only the task snapshot, so the provider is pinned. `resume_session_id` is therefore always that provider's id.
- **Retry with another agent**: `POST /tasks/{id}/rerun {agent_profile_id, prompt?}` creates a **new task** (`parent_task_id = id`, same project and repository, `title` and `prompt` copied unless given). Its branch is `agent/<new id>-<slug>`, created from the parent's branch tip: the remote branch if it was pushed, else the parent workspace's local branch, else the base branch (Q6). Allowed when the parent is not active (`paused`, `pr_open`, `completed`, `failed`, `cancelled`). The parent is left as is. A `rerun` backend event `{task_id}` is added to the parent's timeline.

### 2.3 Profile options (validated per provider)

| key | claude | codex | gemini |
|---|---|---|---|
| `effort` | `low medium high xhigh max` | `minimal low medium high xhigh max` (the `ultra`/`persistent` levels are left out until verified) | rejected (unsupported) |
| `max_turns` | int > 0 | int > 0 (adapter-enforced) | int > 0 (`model.maxSessionTurns`) |
| `system_prompt` | appended (after project instructions) | `developer_instructions` | global `GEMINI.md` |
| `web_search` | bool (default true) | `webSearchMode` `live`/`disabled` | bool → exclude the web tools |

Unknown keys are `400 invalid`. `model` is a free string (≤ 100 chars, `[A-Za-z0-9._:/-]`) because model lists change faster than the code. Server-side lists are hints only (`GET /agent-providers`).

### 2.4 Credentials

| Server env (new or existing) | Runner gets | Only for |
|---|---|---|
| `CLAUDE_CODE_OAUTH_TOKEN` / `ANTHROPIC_API_KEY` (existing) | same names | claude |
| `CODEX_API_KEY`, falling back to `OPENAI_API_KEY` (already used by STT) | `CODEX_API_KEY` | codex |
| `GEMINI_API_KEY` | `GEMINI_API_KEY` | gemini |

- `credential_status` = `configured` when the variable(s) for that provider are non-empty at startup. The API never accepts or returns key values.
- `POST /tasks` and `rerun` with an unconfigured provider → `503 credential_missing` ("Codex is not configured: set OPENAI_API_KEY or CODEX_API_KEY on the server"), matching the existing `503 github_disabled` style. If a key is removed later, the scheduler fails the run with `failed: credential_missing` before starting a container.
- The runner env is built per provider by an explicit allow-list, so a Codex runner never sees the Claude token and vice versa. Project `env` reserves all of these names plus `*_BASE_URL`, `CODEX_HOME`, `GEMINI_CLI_HOME`, `GEMINI_CLI_SYSTEM_SETTINGS_PATH`, `GEMINI_SYSTEM_MD`.
- Fake mode (`ANORA_RUNNER_FAKE=1`) treats every provider as configured.

**Credential-injecting egress proxy (design only, not built).** A small `anora-egress` container on each task network would be the only route to the three API hosts. Runners would get a dummy key and a base URL pointing at the proxy (`http://egress:8080/<provider>`). The proxy swaps in the real header (`x-api-key`/`Authorization: Bearer`/`x-goog-api-key`) and forwards over TLS. Keys would then never be in the runner's env, which closes the "`node -e process.env`" residual risk in DECISIONS. Per provider: Claude `ANTHROPIC_BASE_URL` works (the OAuth subscription token would need the proxy to set `Authorization: Bearer`; verify), Codex `openai_base_url`/SDK `baseUrl` works, Gemini `GOOGLE_GEMINI_BASE_URL` works. Prerequisites: an egress allow-list for the API hosts, and a per-task proxy secret so another task cannot use it. Web search and fetch tools that the provider hosts are unaffected.

### 2.5 Agent

```
agent/src/
  adapter.ts          // interface + shared outcome types (replaces session.ts)
  adapters/claude.ts  // refactor of claude.ts + mapper.ts, no behaviour change
  adapters/codex.ts   // @openai/codex-sdk
  adapters/gemini.ts  // spawns gemini, parses stream-json
  ask/bridge.ts       // unix-socket server in the agent: question/answer
  ask/mcp-server.ts   // stdio MCP server "anora" with tool ask_owner, spawned by the CLI
  hooks/pretool.ts    // CLI-agnostic policy hook entry point (reads JSON on stdin)
  usage.ts            // per-session usage ledger in the state dir + pricing
```

```ts
interface Adapter {
  readonly provider: "claude" | "codex" | "gemini";
  checkAuth(env): { ok: true } | { ok: false; message: string };
  // start or resume (cmd.resume_session_id); yields protocol events, resolves to an outcome
  run(ctx: { cmd: RunCommand; workspace: string; emitter: Emitter; control: Control; ask: AskBridge }): Promise<SessionOutcome>;
  interrupt(): void;   // claude: q.interrupt(); codex/gemini: abort the child, outcome "done: Interrupted"
  cancel(): void;      // abort, outcome "cancelled"
}
```

- **Questions**: `ask_owner({question, options?})`. For Codex and Gemini it is a stdio MCP server (`node /app/dist/ask/mcp-server.js`) that the CLI spawns. It forwards the call over a Unix socket (`$XDG_RUNTIME_DIR` or `/tmp/anora-ask-<pid>.sock`, mode 0600) to the agent, which emits `question`, waits for `answer`, and returns the text as the tool result. Claude gets the same tool in-process (`createSdkMcpServer`), and `AskUserQuestion` is removed from its tools, so all three behave the same. Timeouts: Codex `tool_timeout_sec`, Gemini `timeout`, Claude `MCP_TOOL_TIMEOUT`, each set to `question_timeout_sec + 60`. On the question timeout the agent returns "not answered", aborts the session and emits `paused` (exit 3) as today. A socket is a listener but not a network one (DECISIONS entry).
- **Policy**: `policy.ts` stays the single rule set. Claude keeps it in-process (`PreToolUse`). Codex and Gemini call `node /app/dist/hooks/pretool.js` from `PreToolUse` (Codex) or `BeforeTool` (Gemini). Their tool names are mapped onto the existing ones: Codex `Bash`/`apply_patch` and Gemini `run_shell_command`/`write_file`/`replace`. See F2/F3 for the caveats.
- **Config isolation**: the agent writes each CLI's config into the state dir at run start (Codex: `--config` overrides via the SDK; Gemini: a system settings file at `GEMINI_CLI_SYSTEM_SETTINGS_PATH=/tmp/anora/gemini-system.json`) with our MCP server, hooks, trust and turn limit. Repo-level `.codex/`/`.gemini/` config still loads, like Claude's `settingSources: ["project"]`. See Q8.
- **Sandbox**: Codex `sandboxMode: "danger-full-access"`, `approvalPolicy: "never"`. Gemini `--approval-mode yolo`, `--skip-trust`. The container is the sandbox, as it is for Claude today (F4).
- **Event mapping** (no new event types):

| protocol | Codex | Gemini |
|---|---|---|
| `started{session_id, model, provider}` | `thread.started` (+ model from profile/config) | `init` |
| `assistant_text` | `item.completed` `agent_message` | `message` role=assistant (deltas joined until the next non-message event) |
| `tool_use{tool, summary}` | `item.started` `command_execution` → `Bash`, "`$ <cmd>`" (cut); `file_change` → `Edit`, "Edit a.ts (+2 files)"; `mcp_tool_call` (not ask_owner) → `mcp:<server>.<tool>`; `web_search` → `WebSearch` | `tool_use` `tool_name` → mapped names (`run_shell_command`→`Bash`, `write_file`→`Write`, `replace`→`Edit`, `read_file`→`Read`, …; unknown names verbatim) |
| `tool_result{ok, summary?}` | `item.completed` with `status`/`exit_code` | `tool_result.status` |
| `usage` | each `turn.completed` (ledger-cumulative, 2.6) | `result.stats` |
| `done.summary` | last `agent_message` | joined assistant text of the last turn |
| limit (exit 4) | adapter turn/budget counter → abort | exit 53 or the adapter budget counter |
| `error{code:"sdk_error"}` | `turn.failed`, `error`, non-zero exit | `result.status=error`, exit 1/42 |

  `reasoning` and `todo_list` items are not emitted (Claude's thinking is not emitted either).
- **Turns** for Codex: one completed `command_execution`/`file_change`/`mcp_tool_call`/`agent_message` item counts as one model round-trip. This is an approximation, recorded in DECISIONS.
- **Fake mode** ignores the provider for behaviour but echoes it in `started.provider` and uses a provider-shaped session id, so the dev stack and the tests run without keys.

### 2.6 Usage and cost

- The `usage` event gains optional `cached_input_tokens`. `cost_usd_total` becomes **nullable** (also in `done`).
- **Session-cumulative semantics are kept for every provider**: the agent keeps a ledger `<state dir>/anora-usage/<session_id>.json` in the provider's state volume and adds each run's tokens to it, so the backend's existing "per-run = this total − previous total" logic is unchanged.
- **Cost**: Claude reports it. For Codex and Gemini the server sends `run.pricing{input_usd_per_mtok, cached_input_usd_per_mtok, output_usd_per_mtok}` from its price table, the agent computes the cost, and the agent enforces `max_budget_usd` with it. Without a price entry `cost_usd_total` is `null`, the budget limit is not enforced (the server writes one `status`-style warning to the timeline; Q3), and `runs.cost_known=false`. This deviates slightly from "the server computes it": the agent must compute it to enforce the budget mid-run, and both use the server's one table.
- Price table: `AGENT_PRICING` env (JSON), `{"codex:gpt-5.5":{"input":…,"cached_input":…,"output":…},"gemini:*":{…}}`, where `<provider>:*` is a fallback. A default file is not shipped, because prices change and invented numbers would be wrong.
- Metrics: `anora_cost_usd_total{project, provider}`, `anora_tokens_total{provider, kind=input|output}`, a `provider` label on `anora_run_duration_seconds` and `anora_runner_exits_total`.

### 2.7 Images and volumes

**One image with all three adapters (recommended).** Size: Claude 265 MB, Codex 430 MB, Gemini 99 MB unpacked on top of a ~2 GB Playwright base, so about +0.5 GB (+25 %). Build time: one `npm ci`, cached, with no extra build stages. Attack surface: the extra binaries only run when selected and get no extra privilege or key, because the key is per provider and per run. One image keeps `RUNNER_IMAGE`, `projects.runner_image`, the release pull and the reconciler unchanged; three images would triple pull and deploy work and complicate project image overrides. The alternative (one image per provider) only pays off if size becomes a problem. It can be added later as `RUNNER_IMAGE_<PROVIDER>` without a protocol change.

State volumes: `anora-<provider>-<task>` (Claude keeps its current name `anora-claude-<task>`), label `anora.kind=agent-state` (cleanup also matches the legacy `claude-state`). Mounts: claude `/home/runner/.claude`, codex `/home/runner/.codex` with `CODEX_HOME` set, gemini `/home/runner/.gemini`. Retention removes them by label.

---

## 3. Proposed spec changes

### 3.1 `docs/SPEC-server.md`

```diff
@@ section 3, Containers
-| `anora` (Go) | permanent | … | GitHub App key, Claude token, Telegram token |
+| `anora` (Go) | permanent | … | GitHub App key, agent provider keys (Claude, OpenAI, Gemini), Telegram token |
-| `anora-agent-<task>` | one run | `anora-task-<task>` | Claude auth only |
+| `anora-agent-<task>` | one run | `anora-task-<task>` | the key of the task's provider only |
@@ Key rules
-… It has internet egress (Anthropic API, npm, Go proxy, PyPI).
+… It has internet egress (the provider API, npm, Go proxy, PyPI).
@@ Task lifecycle 1.
-1. `queued`: created via API or Telegram.
+1. `queued`: created via API or Telegram. The agent profile is resolved (task → project default → global default) and `provider`, `model`, `agent_options` are snapshotted onto the task; they never change for that task.
@@ 5. paused
-… workspace and Claude state volume kept …
+… workspace and the provider's state volume kept …
@@ section 4 (title and intro)
-The runner is a Node.js process … runs exactly one Claude Code session through `@anthropic-ai/claude-agent-sdk` …
+The runner is a Node.js process … runs exactly one agent session through a provider adapter: Claude (`@anthropic-ai/claude-agent-sdk`), Codex (`@openai/codex-sdk`) or Gemini (`gemini` CLI, headless `stream-json`). The `run` command names the provider. …
@@ Auth
-Use `CLAUDE_CODE_OAUTH_TOKEN` … else `ANTHROPIC_API_KEY`. Fail fast with `error{code:"no_auth"}` if neither exists.
+Claude: `CLAUDE_CODE_OAUTH_TOKEN`, else `ANTHROPIC_API_KEY`. Codex: `CODEX_API_KEY`. Gemini: `GEMINI_API_KEY`. Fail fast with `error{code:"no_auth"}` when the selected provider's key is missing.
@@ Protocol tables: see PROTOCOL.md diff (run: provider, options, pricing; started: provider; usage: cached_input_tokens, nullable cost)
@@ Behaviour 2–5
+2a. Claude: as before. Codex: `startThread`/`resumeThread` with `workingDirectory`, `model`, `modelReasoningEffort`, `sandboxMode: "danger-full-access"`, `approvalPolicy: "never"`. Gemini: `gemini -p … --output-format stream-json [--resume <id>] [--model …] --approval-mode yolo --skip-trust`.
-5. Questions: when Claude uses its ask-the-user tool, emit `question` …
+5. Questions: every provider gets the MCP tool `ask_owner` from the runner's own MCP server; a call emits `question`, waits for `answer` … (unchanged timeout and pause rules).
-6. Limits: … Use an SDK budget option if one exists; otherwise enforce from the reported cost.
+6. Limits: … Use the provider's turn/budget option where it exists; otherwise the adapter enforces turns and budget (cost from `run.pricing`).
@@ Fake mode
-… do not call Claude …
+… do not call any provider; accepts every `provider` …
@@ Dockerfile
-… Claude state in `/home/runner/.claude` (mounted per task by the backend).
+… one image with all adapters. Provider state in `/home/runner/.claude`, `/home/runner/.codex` (`CODEX_HOME`) or `/home/runner/.gemini`, mounted per task.
@@ section 5, Configuration
-| `CLAUDE_CODE_OAUTH_TOKEN` / `ANTHROPIC_API_KEY` | — | passed to runners only |
+| `CLAUDE_CODE_OAUTH_TOKEN` / `ANTHROPIC_API_KEY` | — | passed to Claude runners only |
+| `CODEX_API_KEY` (else `OPENAI_API_KEY`) | — | passed to Codex runners only, as `CODEX_API_KEY` |
+| `GEMINI_API_KEY` | — | passed to Gemini runners only |
+| `AGENT_PRICING` | empty | JSON price table for providers that report no cost |
-| `DEFAULT_MODEL` | empty (SDK default) |  |
+| `DEFAULT_MODEL` | empty | seeds the model of the default Claude profile once; profiles own models afterwards |
@@ Runner manager
-- Create volume `anora-claude-<id>` mounted at `/home/runner/.claude` …
+- Create volume `anora-<provider>-<id>` (label `anora.kind=agent-state`) mounted at the provider's state dir …
-… project env plus auth env.
+… project env plus the auth env of the task's provider only.
@@ Workspace cleanup
-Delete the workspace and the Claude state volume 7 days after …
+Delete the workspace and the agent state volume 7 days after …
@@ section 6, schema: add agent_profiles, projects.default_agent_profile_id, tasks.{agent_profile_id, provider, model, agent_options, parent_task_id, input_tokens, output_tokens}, runs.{input_tokens, output_tokens, cost_known} (2.1); drop projects.model
@@ section 7, Endpoints
+| `GET /api/v1/agent-profiles` · `POST` | list, create |
+| `GET /api/v1/agent-profiles/{id}` · `PATCH` · `DELETE` | `DELETE` is `409 in_use` while it is a project or global default |
+| `GET /api/v1/agent-providers` | providers with `credential_status`, option keys and model hints |
-| `GET /api/v1/tasks?project=&status=` · `POST` | list, create `{project_id, repository_id?, title?, prompt}` |
+| `GET /api/v1/tasks?project=&status=&provider=` · `POST` | list, create `{project_id, repository_id?, title?, prompt, agent_profile_id?}` |
+| `POST /api/v1/tasks/{id}/rerun` | new task from this task's branch with `{agent_profile_id, prompt?}` |
@@ section 8, Commands
-| `/new <project> <prompt>` | create a task …
+| `/new [<project>] [@<profile>] <prompt>` | create a task; without `@<profile>` the project default is used. The draft card has an "Agent: <name>" button that cycles through profiles |
+| `/agents` | list profiles with provider, model and credential status |
@@ Messages
+- Task cards and notifications show the provider and model ("Codex · gpt-5.5"). Finished cards get "Retry with…", which opens a profile picker and calls the same service as `rerun`.
@@ section 9, Observability
-… cost total per project …
+… cost total per project and provider, tokens per provider, a `provider` label on run duration and exits …
```

### 3.2 `docs/SPEC-web.md`

```diff
@@ 1. Tasks (home)
-- Card per task: title, project, status badge, …
+- Card per task: title, project, status badge, provider badge (Claude/Codex/Gemini, small, monochrome icon + name), …
@@ 2. New task
+- Agent picker (chip under the prompt): defaults to the project's default profile; lists profiles with provider and model; profiles whose provider has `credential_status: "missing"` are shown disabled with "Key not configured".
@@ 3. Task detail
-- Header: title, status badge, project/repo, branch, cost and turns, …, overflow menu (Cancel, Interrupt, Retry, Copy branch, Open on GitHub).
+- Header: title, status badge, provider badge + model, project/repo, branch, cost (or "tokens only" when cost is unknown) and turns, …, overflow menu (Cancel, Interrupt, Retry, **Retry with another agent**, Copy branch, Open on GitHub). "Retry with another agent" opens a bottom sheet with the profile list and an optional prompt edit, then navigates to the new task; the new task's Info tab links to its parent.
-- Composer: … "Message Claude…" while running …
+- Composer: … "Message <provider>…" …; running indicator "<Provider> is working".
@@ 4. Projects
-… model, limits …
+… default agent (profile select; "Server default" = global default), limits …
@@ 5. Settings
+- **Agents**: list of profiles (name, provider badge, model, default star, credential status). Add/edit in a full-screen form at 375 px: name, provider (segmented; locked after creation, Q9), model (text with provider hints from `GET /agent-providers`), effort (only the provider's values), max turns, extra system prompt, web search toggle; "Make default"; delete (disabled with the reason when `in_use`).
```

### 3.3 `docs/PROTOCOL.md`

```diff
-| `run` | `task_id`, `prompt`, `append_system_prompt?`, `resume_session_id?`, `model?`, `limits{…}`, `commit{…}` |
+| `run` | `task_id`, `provider?` (`claude` default, `codex`, `gemini`), `prompt`, `append_system_prompt?`, `resume_session_id?`, `model?`, `options?{effort?, web_search?}`, `limits{max_turns, max_budget_usd, max_duration_sec, question_timeout_sec}`, `pricing?{input_usd_per_mtok, cached_input_usd_per_mtok, output_usd_per_mtok}`, `commit{…}` |
-| `started` | `session_id`, `model` |
+| `started` | `session_id`, `model`, `provider` |
-| `usage` | `cost_usd_total`, `input_tokens`, `output_tokens`, `turns` |
+| `usage` | `cost_usd_total` (number or `null`), `input_tokens`, `output_tokens`, `cached_input_tokens?`, `turns` |
-| `done` | `session_id`, `summary`, `no_changes`, `cost_usd_total` |
+| `done` | `session_id`, `summary`, `no_changes`, `cost_usd_total` (number or `null`) |
-Error codes: `no_auth`, `bad_command`, …
+Error codes: `no_auth`, `bad_command`, `unsupported_option` (an option the provider cannot honour), …
@@ Notes
-- `question_id` … One `AskUserQuestion` call with several questions produces several `question` events …
+- Questions come from the runner's `ask_owner` MCP tool (one question per call) for every provider.
-- `cost_usd_total` … is cumulative for the Claude **session** …
+- `cost_usd_total` and the token counts are cumulative for the provider **session** (the runner keeps a ledger in the state volume for providers that report per-turn usage). `null` cost = the provider reports none and `run.pricing` was absent.
-- `limits.max_budget_usd` is passed to the SDK (`maxBudgetUsd`) …
+- `limits.max_budget_usd`: Claude passes it to the SDK; Codex and Gemini enforce it from `pricing` (not enforced without pricing).
+- `interrupt`: Codex and Gemini cannot interrupt a turn softly; the runner stops the CLI process, which also ends the run as `done` ("Interrupted by the user."), so the outcome is the same.
@@ Environment
-| `CLAUDE_CODE_OAUTH_TOKEN` / `ANTHROPIC_API_KEY` | Claude auth … |
+| `CLAUDE_CODE_OAUTH_TOKEN` / `ANTHROPIC_API_KEY` | Claude auth; the OAuth token wins |
+| `CODEX_API_KEY` | Codex auth |
+| `GEMINI_API_KEY` | Gemini auth |
+| `CODEX_HOME` | set by the backend to `/home/runner/.codex` (state volume) |
-| `ANORA_RUNNER_FAKE=1` | scripted session, no Claude: …
+| `ANORA_RUNNER_FAKE=1` | scripted session for any `provider`, no API calls: …
```

### 3.4 `docs/API.md`

```diff
+## Agent profiles
+
+```json
+{"id": 1, "name": "Claude", "provider": "claude", "model": null,
+ "options": {"effort": "high", "max_turns": 150, "system_prompt": "", "web_search": true},
+ "is_default": true, "credential_status": "configured", "in_use": true,
+ "created_at": "…", "updated_at": "…"}
+```
+
+| | |
+|---|---|
+| `GET /agent-profiles` | all profiles, default first then by name (no pagination; a handful exist) |
+| `POST /agent-profiles` `{name, provider, model?, options?, is_default?}` | create; `400 invalid` for an unknown provider, option or effort value |
+| `GET /agent-profiles/{id}` | one profile |
+| `PATCH /agent-profiles/{id}` `{name?, model?, options?, is_default?}` | `provider` is immutable (`400 invalid`); `is_default: true` moves the default; `false` on the default is `409 conflict`. Running tasks are unaffected (snapshot) |
+| `DELETE /agent-profiles/{id}` | `409 in_use` while it is the global default or a project's default |
+| `GET /agent-providers` | `{"items":[{"provider":"codex","credential_status":"missing","options":["effort","max_turns","system_prompt","web_search"],"effort_values":[…],"model_hints":[…]}]}` |
+
+Keys are never sent or returned. `credential_status` is `configured` or `missing` (server env).
@@ Projects
-  "runner_image": null, "model": null, "max_turns": null, …
+  "runner_image": null, "default_agent_profile_id": null, "max_turns": null, …
+`default_agent_profile_id`: `null` = the global default profile. `PATCH` with `0` resets it.
@@ Tasks object
+  "agent_profile_id": 1, "provider": "claude", "model": null, "parent_task_id": null,
+  "input_tokens": 120345, "output_tokens": 8012,
+- `provider` and `model` are fixed when the task is created (`model: null` = the provider's default). Follow-ups, resumes and retries always use them; use `rerun` to try another agent.
+- `cost_usd_total` sums the runs whose cost is known; `cost_complete: false` when a run reported tokens only.
@@ Errors
+  | 503 | `github_disabled`, `telegram_disabled`, `credential_missing` | integration or provider key not configured |
@@ Endpoints
-| `POST /tasks` `{project_id, prompt, repository_id?, title?}` | …
+| `POST /tasks` `{project_id, prompt, repository_id?, title?, agent_profile_id?}` | … The profile defaults to the project's, then the global default. `503 credential_missing` when its provider has no key |
+| `POST /tasks/{id}/rerun` `{agent_profile_id, prompt?}` | not in `queued`/`preparing`/`running`/`waiting_input`/`pushing` (`409 conflict`): creates a new task (`parent_task_id` = this one) on a new branch started from this task's branch, returns the new task (`201`) |
@@ Runs
-`{id, task_id, kind, input, container_id, exit_code, last_seq, cost_usd, turns, …}`
+`{…, cost_usd (number or null), input_tokens, output_tokens, turns, …}`
@@ Backend events
-| `created` | `{attachments}` … |
+| `created` | `{attachments, provider, model, agent_profile_id, parent_task_id?}` (the agent fields are absent in older timelines) |
+| `rerun` | `{task_id}`: a new task was started from this one with another agent |
@@ Runner events note
+`started` carries `provider`; `usage`/`done` `cost_usd_total` may be `null`.
@@ Stats
+`GET /stats` adds `by_provider: [{provider, running, cost_usd_month, tokens_month}]`.
```

### 3.5 `docs/openapi.yaml` (summary of the schema diff)

- New paths: `/api/v1/agent-profiles` (get, post), `/api/v1/agent-profiles/{id}` (get, patch, delete), `/api/v1/agent-providers` (get), `/api/v1/tasks/{id}/rerun` (post). All take the `X-Requested-With` header parameter on non-GET like every other path.
- New schemas: `Provider` (enum `claude|codex|gemini`), `AgentOptions` (`effort`, `max_turns`, `system_prompt`, `web_search`; `additionalProperties: false`), `AgentProfile`, `AgentProfileCreate`, `AgentProfileUpdate`, `CredentialStatus` (enum), `AgentProviderInfo`, `RerunRequest`, `RerunEvent` + `RerunPayload`.
- Changed: `Project` / `ProjectCreate` / `ProjectUpdate` (+`default_agent_profile_id`, −`model`), `Task` (+`agent_profile_id`, `provider`, `model`, `parent_task_id`, `input_tokens`, `output_tokens`, `cost_complete`), `TaskCreate` (+`agent_profile_id`), `Run` (`cost_usd` nullable, +`input_tokens`, `output_tokens`), `StartedPayload` (+`provider`), `UsagePayload` (`cost_usd_total` nullable, +`cached_input_tokens`), `DonePayload` (`cost_usd_total` nullable), `CreatedPayload` (+agent fields), `Stats` (+`by_provider`), the `Event` `oneOf` + discriminator (+`rerun`), error code docs (+`credential_missing`).
- `make gen-api` regenerates `web/src/api/schema.ts`; the server's OpenAPI test reads the file.

### 3.6 `docs/DECISIONS.md` entries (to add with the code)

**Server**
- Agent profiles are a table with `is_default` (partial unique index) instead of a settings table: the default is one row's property, and the API stays CRUD-only.
- Tasks snapshot `provider`, `model` **and `agent_options`**: editing a profile mid-task would otherwise change effort or system prompt under a running conversation.
- `DEFAULT_MODEL` now only seeds the default Claude profile once at startup (SQL migrations cannot read env); `projects.model` is migrated into per-project profiles and removed from the API.
- "Retry with another agent" is a new task (`rerun`, `parent_task_id`) on a new branch from the parent's branch, never a provider change inside a task, because session ids and state volumes are provider-specific.
- `503 credential_missing` follows the existing `503 github_disabled` pattern for "the server lacks configuration".
- Codex key: `CODEX_API_KEY`, falling back to `OPENAI_API_KEY`, because `OPENAI_API_KEY` already serves STT and the owner may want separate keys or one key.
- Cost for providers without USD reporting comes from `AGENT_PRICING`, sent to the runner as `run.pricing`, so the runner can enforce the budget mid-run; without an entry, cost is `null` and the budget is not enforced (turns and duration still are).
- Metrics gain a `provider` label; `anora_cost_usd_total` counts only known costs.

**Agent**
- One image with all three adapters: about +0.5 GB on a ~2 GB base, one build and pull, and unused CLIs gain no privilege or key; per-provider images can come later via `RUNNER_IMAGE_<PROVIDER>`.
- `ask_owner` is a runner-owned MCP tool for all providers (Claude in-process via `createSdkMcpServer`; Codex/Gemini via a stdio MCP shim talking to the runner over a 0600 Unix socket). `AskUserQuestion` is no longer offered to Claude, so questions behave the same everywhere. A Unix socket is not a network listener (SPEC section 4).
- Codex runs with `sandboxMode: "danger-full-access"`, `approvalPolicy: "never"` and Gemini with `--approval-mode yolo`: the container is the sandbox (as for Claude), and the nested Linux sandboxes need namespaces our hardened container does not grant.
- Codex has no max-turns option: the adapter counts completed model items as turns and aborts on the limit.
- Codex and Gemini have no soft interrupt; `interrupt` stops the CLI process and the run ends as `done` ("Interrupted by the user."), the same observable result as Claude.
- Usage stays session-cumulative for all providers through a per-session ledger in the state volume, so the backend's per-run cost arithmetic is unchanged.
- Gemini gets the extra system prompt through the global `GEMINI.md` in its state dir, because `GEMINI_SYSTEM_MD` replaces the whole system prompt; `effort` is rejected for Gemini (no CLI setting).
- State volumes are `anora-<provider>-<task>` with label `anora.kind=agent-state`; Claude keeps its old name so existing paused tasks still resume.

**Web**
- Provider badges are text plus a monochrome glyph, not vendor logos (bundle size, trademark use).

---

## 4. Not feasible or degraded (from the research)

- **F1. No USD cost from Codex or Gemini.** The cost is only as good as `AGENT_PRICING`. Without it, the budget limit is not enforced for those providers.
- **F2. Codex policy hooks.** Hooks exist, but non-managed hooks need persisted trust, and the SDK cannot pass `--dangerously-bypass-hook-trust`. Phase 2 must verify that hooks defined through `--config`, or a managed/requirements config in the image, run in `codex exec` without interaction. If neither works, the Codex adapter runs without the runner policy. The real guarantees still hold: no GitHub token, no Docker socket, `.github/` rejected and secrets scanned server-side. What is lost is the in-session denial of `.github/` writes, `/attachments` writes and env dumps. **Owner decision needed (Q7).**
- **F3. Gemini hooks in headless mode** are not documented. If they don't fire, use `--policy` rules (policy engine) for the shell deny list, or the same fallback as F2.
- **F4. Nested sandboxes.** Codex `workspace-write` and Gemini `--sandbox` cannot be used inside the runner (no Docker; namespaces restricted by `cap_drop: ALL` + `no-new-privileges`). That is acceptable: Claude already relies on the container alone.
- **F5. Key visibility.** Codex's `shell_environment_policy` can hide `CODEX_API_KEY` from shell commands (better than Claude today). Gemini's shell inherits env, which is the same residual risk as Claude. The egress proxy (2.4) would remove it for all three.
- **F6. Gemini effort** is not settable from the CLI. **Gemini's future** is uncertain (Antigravity CLI replaced it for consumer tiers).
- **F7. Turns are not comparable** across providers (Codex is approximated).

## 5. Open questions

1. **Gemini CLI vs Antigravity CLI.** Build on Gemini CLI with `GEMINI_API_KEY` (still released, API keys still supported), or wait and evaluate Antigravity's headless mode? I recommend Gemini CLI now, behind the adapter interface.
2. Codex key: is `CODEX_API_KEY` with an `OPENAI_API_KEY` fallback OK, or should Codex require its own `CODEX_API_KEY`, separate from STT?
3. Cost for Codex/Gemini: is it OK that **the agent** computes it from the server's table (sent as `run.pricing`) so the budget is enforced mid-run, rather than the server computing it after the fact? Without a price entry: allow tasks with an unenforced budget (my proposal), or refuse to start?
4. `projects.model`: migrate it into per-project profiles and remove it from the API (proposed), or keep it as a model override on top of the profile?
5. Limits: should `max_turns` live on the profile (proposed, with project override winning), and budget/duration stay project-only?
6. Rerun base: start the new branch from the parent's branch tip (proposed; needs the branch pushed, or a local copy from the parent's workspace), or from the base branch (a clean second attempt)? Should a rerun open its own PR, or replace the parent's (close it)?
7. If Codex hooks cannot be made to run unattended (F2), is Codex acceptable without in-session policy, relying on the container plus server-side diff validation?
8. Repo-level agent config (`.codex/`, `.gemini/settings.json`): load it (parity with Claude's `settingSources: ["project"]`) or ignore it? A repo could add MCP servers or hooks to itself.
9. Profile `provider` immutable after creation (proposed), or editable when no task references the profile?
10. Telegram: `/new @profile` plus the draft-card button (proposed), or only one of them?
11. Should `AskUserQuestion` stay available to Claude alongside `ask_owner` (its multi-question form is handy), or be removed for uniformity (proposed)?
12. Keep this design file after implementation, or delete it once the specs are updated?

## 6. Test plan (phase 2)

- Server: profile CRUD and validation per provider; resolution order (task > project > global); snapshot immutability (profile edit and delete after creation, follow-up, resume, retry); provider pinning on follow-up; `credential_missing` on create, rerun and schedule; per-provider runner env (no foreign keys); volume naming and cleanup by label; rerun branch creation; migration backfill.
- Agent: adapter conformance tests through a fake child process replaying **recorded JSONL fixtures** (`agent/test/fixtures/codex-*.jsonl`, `gemini-*.jsonl`, captured once by hand with real keys and checked in; no live calls in tests) → protocol events; `ask_owner` bridge round trip and timeout → `paused`; usage ledger across resume; pricing and budget abort → exit 4; hook entry point decisions for each CLI's payload shape.
- Web: profile screens, picker, badge and rerun sheet with MSW; `make e2e` mock mode plus one real-stack test using the fake agent with `provider: codex`.

## 7. Phase 2 commit order

1. Migration 00007 + store/model + bootstrap of the default profile (SPEC-server section 6, DECISIONS).
2. API: profiles, providers, task fields, rerun, `credential_missing`; `openapi.yaml`, `API.md`, `make gen-api`.
3. Protocol fields (TS + Go mirror + PROTOCOL.md) and the adapter interface with the Claude adapter refactored onto it, including `ask_owner` (the only intended Claude behaviour change, Q11). Server: runner env and volumes per provider, pricing, metrics.
4. Codex adapter + fixtures + Dockerfile.
5. Gemini adapter + fixtures + Dockerfile.
6. Telegram.
7. Web.
8. `deploy/.env.example` (new keys, `AGENT_PRICING`) lands with commit 3; `make e2e` at the end.

Sources: [npm @openai/codex-sdk](https://www.npmjs.com/package/@openai/codex-sdk) · [Codex SDK source](https://github.com/openai/codex/tree/main/sdk/typescript) · [Codex non-interactive mode](https://learn.chatgpt.com/docs/non-interactive-mode) · [Codex config reference](https://learn.chatgpt.com/docs/config-file/config-reference) · [Codex hooks](https://learn.chatgpt.com/docs/hooks) · [npm @google/gemini-cli](https://www.npmjs.com/package/@google/gemini-cli) · [Gemini CLI headless](https://geminicli.com/docs/cli/headless/) · [Gemini CLI hooks](https://geminicli.com/docs/hooks/) · [The New Stack: Antigravity replaces Gemini CLI](https://thenewstack.io/gemini-cli-antigravity-replacement/) · [letsdatascience: Google replaces Gemini CLI](https://letsdatascience.com/news/google-replaces-gemini-cli-with-antigravity-cli-574b2056)
