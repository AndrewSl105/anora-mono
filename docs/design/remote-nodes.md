# Design: remote nodes (run tasks on your own computer) and the desktop canvas

Status: implemented in phases (see the end). Date: 2026-10-29.

## 1. Goal

One Fleetpocket: one server, one login, one history, one task list. What changes is **where a task's containers run**. A task started from the phone can run on the VPS; another one runs on the owner's Mac, using the Mac's CPU, memory, Docker and the Mac's own Claude / Codex / Cursor credentials. Every client (web, PWA on the Mac, Telegram, Mini App) sees and controls every task the same way, wherever it runs.

```
phone / Mac PWA / Telegram ──► server (VPS) ── auth, DB, scheduler, GitHub App, bot
                                   │  ├── host "server": local Docker (as today)
                                   │  └── host "<node>": WebSocket ◄── fleetpocket-node (Mac)
                                   │                                     └── Docker Desktop / OrbStack
                                   │                                          ├── agent (task network)
                                   │                                          └── sidecars
```

Not a goal: a second server on the Mac, peer-to-peer clients, or nodes run by anyone but the owner.

## 2. Terms

- **Host**: where one task's workspace, Claude state volume, task network, sidecars and runner container live. `server` (the VPS's Docker, today's behaviour) or a **node**.
- **Node**: a computer that runs `fleetpocket-node`, connected to the server over an outbound WebSocket. Registered by the owner in the web app; identified by a slug (`macbook`).
- **Placement**: the host a task runs on. Fixed when the task is created (its workspace, Claude session and branch history live there), like the agent.

## 3. Server side

### 3.1 Host interface

`internal/hosts` defines `Host`: every workspace and Docker operation the scheduler, publisher, maintenance and diff endpoint used to call on `workspace.Manager` and `runnermgr.Manager` directly (prepare workspace, unpushed commits, diff, diff stat, validate, push, events log, attachments, image check, network, volume, sidecars, start / attach / wait / stop / remove runner, list runners, cleanup). Two implementations:

- `hosts.Local`: the existing managers, unchanged behaviour.
- `hosts.Remote`: the same calls as JSON-RPC over the node's WebSocket.

`hosts.Registry` resolves a task's placement (`tasks.node`, NULL = server) to a `Host`, tracks which nodes are online and publishes node changes to the event hub. A call to an offline node returns `hosts.ErrOffline`.

### 3.2 Scheduling

- `tasks.node` (NULL = server) and `projects.node` (NULL = server) are new columns. `POST /tasks` takes `node`: omitted → the project's, `"server"` → the VPS, else a node id.
- `ClaimNext` claims the oldest queued task whose host has room: server tasks count against `MAX_CONCURRENT_TASKS`, a node's tasks against that node's `max_concurrent`; tasks of offline nodes are skipped (they wait in `queued`, the UI shows "waiting for <node>").
- If the node drops between claim and start, the task goes back to `queued` instead of failing.
- Agent check at creation: a server task needs the agent's key on the server (as today); a node task needs the agent among the node's reported agents (last `hello`, stored in `nodes.agents`).

### 3.3 Runner streams and restart safety

`runnermgr.Runner` is generalised to any line stream (`NewStreamRunner`). For a remote runner the node forwards the container's stdout lines; the server parses them with the same `ParseEvent` and the scheduler's `supervise` loop is unchanged. A remote runner's event channel stays open across WebSocket drops and closes only when the node reports the container's exit.

- **WebSocket drop, server keeps running**: the node keeps the container running and buffers nothing; on reconnect it sends `hello` with its runners; for each runner the server already supervises it asks the node to re-attach from `MAX(seq)`: the node sends the missing lines from `<workspace>/.fleetpocket/events.jsonl`, then live output. Duplicates are dropped by `UNIQUE (run_id, seq)`, as today.
- **Server restart**: startup reconciliation covers server tasks only. A node's tasks are reconciled when the node says `hello`: runners are re-attached (or their exit applied), live tasks of that node without a runner fail as `lost`, orphans are removed. Tasks of a node that stays offline keep their status.
- **Runner exits while offline**: the node does not remove exited containers; `hello` reports them with the exit code and the server applies it (the same path as an exit while the server was down).

### 3.4 Node API

- `GET /api/v1/node/connect`: WebSocket. `Authorization: Bearer fpn_<node>_<secret>`. The server keeps only the SHA-256 of the secret (`nodes.token_hash`); the token is shown once at registration. Max message 64 MB (attachments).
- Owner API: `GET /nodes`, `POST /nodes {id, name}` → `{node, token}`, `PATCH /nodes/{id} {name?, max_concurrent?}`, `POST /nodes/{id}/token` (rotate), `DELETE /nodes/{id}` (409 while it has live tasks). Node snapshots go out on the WebSocket as `{"op":"node","node":{…}}` to `scope: "tasks"` subscribers.
- Wire format (one JSON object per WebSocket text message):
  - node → server: `{"op":"hello","version","platform","agents":[…],"runners":[{id,task_id,run_id,running,exit_code}]}`, `{"op":"line","container","line"}`, `{"op":"exited","container","code"}`, `{"op":"result","id","result"|"error"}`.
  - server → node: `{"op":"call","id","method","params"}` (methods mirror `Host`), `{"op":"welcome","node"}`.

## 4. Security

The rules of SPEC-server §3 and §5 hold on the node: the agent container gets no GitHub token, no Docker access, no host network (task bridge network, `CapDrop: ALL`, `no-new-privileges`, PID / memory / CPU limits, same code path as the server).

Deliberate differences (recorded in DECISIONS.md):

1. **GitHub token passes through the node.** The GitHub App key stays on the server. For clone, fetch and push the server mints a short-lived installation token for that one repository and sends it in the call; the node uses it for that one git command (`-c http.extraHeader`, as the server does) and never stores it. The agent never sees it.
2. **Agent credentials come from the node.** The server never sends Claude, OpenAI or Cursor keys to a node; the node adds its own (environment, or the macOS Keychain) to its runners. Usage counts against the Mac's subscription.
3. **Diff validation and the secrets scan run on the node** (the workspace is there). If the server requires gitleaks (`SECRETS_SCAN=gitleaks`) and the node has no `gitleaks`, publishing fails with a message saying so.
4. **Docker Desktop's host gateway.** On macOS a container can reach `host.docker.internal` (the Mac's own localhost services) through Docker Desktop / OrbStack. The node maps `host.docker.internal` to an unroutable address in runners, which stops name-based access; the gateway IP itself cannot be closed from inside Docker. Do not run sensitive services bound to all interfaces on a node.
5. **Node token at rest.** `fleetpocket-node` reads its token from `FLEETPOCKET_NODE_TOKEN` or the macOS Keychain (`security add-generic-password -s fleetpocket-node -a token -w`), never from a file it writes.

## 5. fleetpocket-node

A Go binary in the server module (`cmd/fleetpocket-node`), reusing `runnermgr` and `workspace`. One process, outbound only, reconnects with backoff.

```
fleetpocket-node run --server https://fleetpocket.example.com
```

| Setting | Default |
|---|---|
| `FLEETPOCKET_SERVER` / `--server` | required |
| `FLEETPOCKET_NODE_TOKEN` | else Keychain `fleetpocket-node` / `token` |
| `FLEETPOCKET_NODE_DIR` | `~/Library/Application Support/Fleetpocket` (macOS), `~/.local/share/fleetpocket` |
| `DOCKER_HOST` | Docker's default socket |
| `CLAUDE_CODE_OAUTH_TOKEN` / `ANTHROPIC_API_KEY` / `OPENAI_API_KEY` / `CURSOR_API_KEY` | else Keychain `fleetpocket-node` / `<VAR>` |
| `RUNNER_IMAGE_OVERRIDE` | none (use the server's image name; needs `linux/arm64` on Apple Silicon) |
| `SECRETS_SCAN` | `gitleaks` when on `PATH` |

While a runner is active on macOS the node holds `caffeinate -i` so the Mac does not idle-sleep. `deploy/node/com.fleetpocket.node.plist` is a launchd agent for start at login.

## 6. Web: hosts and the canvas

- **Settings → Computers**: register a node (shows the token once and the install command), see online status, agents, capacity and running tasks, rotate or remove.
- **Where to run** select on the project form and the new-task form (`Server` / each node, offline ones marked), and on the Telegram draft card (cycles like Agent).
- Task card, header and info show the host (a small chip: server / laptop icon + name).
- **Canvas** (`/canvas`, desktop; on narrow screens it falls back to the task list): an infinite pan / zoom board of terminal windows, one per task. Each window is a live terminal-style view of the task's timeline (agent text, tool calls, questions with inline answers, a message box), with a title bar showing status, agent and host. Windows can be dragged, resized, minimised to a chip, focused (opens the full task page) and closed (hidden from the canvas until the task changes again). Layout: new tasks land in their host's column (server, then one column per node), so the fleet reads left to right by machine; positions and zoom are remembered per browser (localStorage). A toolbar filters by host / status, "fit all" and "tidy" (re-run the column layout).

## 7. Phases

1. Host interface + local host (no behaviour change).
2. Nodes table, node WebSocket, remote host, placement in scheduling and API.
3. `fleetpocket-node` binary, launchd plist, arm64 images in the release workflow.
4. Web: computers settings, placement selects, host chips, canvas.
5. Telegram placement button; PWA manifest + service worker.
