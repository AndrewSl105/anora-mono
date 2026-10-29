import type { Agent, ApiEvent, Attachment, HostNode, Project, Question, Repository, Run, Task, TaskStatus, TaskView } from '@/api/types'

// In-memory backend for mock mode (VITE_MOCK=1) and tests. Shapes follow
// docs/openapi.yaml; the OpenAPI file has no examples to generate from.

const now = Date.now()
const ago = (min: number) => new Date(now - min * 60_000).toISOString()

function repo(id: number, project_id: string, full_name: string, is_default: boolean, base_branch = 'dev'): Repository {
  return { id, project_id, full_name, base_branch, installation_id: 123, is_default }
}

/** Agents with credentials in mock mode: Cursor has none, so the UI shows that case too. */
export const MOCK_AGENTS = { agents: ['claude', 'codex'] as Agent[], default_agent: 'claude' as Agent }

function project(id: string, name: string, repositories: Repository[], extra: Partial<Project> = {}): Project {
  return {
    id,
    name,
    instructions: '',
    services: [],
    env: {},
    runner_image: null,
    agent: null,
    node: null,
    model: null,
    max_turns: null,
    max_budget_usd: null,
    max_duration_sec: null,
    runner_memory: null,
    runner_cpus: null,
    telegram_thread_id: null,
    archived: false,
    created_at: ago(60 * 24 * 30),
    updated_at: ago(60 * 24 * 2),
    repositories,
    ...extra,
  }
}

type Db = {
  projects: Project[]
  nodes: HostNode[]
  tasks: Task[]
  events: Map<number, ApiEvent[]>
  runs: Map<number, Run[]>
  questions: Map<number, Question[]>
  attachments: Map<number, MockAttachment>
  nextEventId: number
  nextTaskId: number
  nextRepoId: number
  nextRunId: number
}

export const db: Db = {
  projects: [],
  nodes: [],
  tasks: [],
  events: new Map(),
  runs: new Map(),
  questions: new Map(),
  attachments: new Map(),
  nextEventId: 1,
  nextTaskId: 1,
  nextRepoId: 10,
  nextRunId: 1,
}

export type MockAttachment = { taskId: number; meta: Attachment; bytes: Uint8Array; deleted: boolean }

let nextAttachmentId = 1

/** Stores an image like the backend (in memory) and returns its event metadata. */
export function addAttachment(taskId: number, name: string, mime: string, bytes: Uint8Array, width: number, height: number): Attachment {
  const meta: Attachment = { id: nextAttachmentId++, name, mime: mime as Attachment['mime'], size: bytes.byteLength, width, height }
  db.attachments.set(meta.id, { taskId, meta, bytes, deleted: false })
  return meta
}

/** A small UI mockup (160×100 PNG) for seeded attachments. */
const DEMO_PNG = Uint8Array.from(atob('iVBORw0KGgoAAAANSUhEUgAAAKAAAABkCAIAAACO1KzYAAABAklEQVR42u3dsQmAMBRF0ezkBC6jw1rbq5WljSCSQhdQBBHC98AdIOR0eUVS1QwKXHIFgAVYgAVYgAVYgAELsAALsAALsAD/FnjbswIHGLAAC7AAC7AA6xnwNC9FdXXOrh91BhgwYMCAAQMGDBgwYMCAAQMGDBgwYMCAvwNW8LFBgAEDBgwYMGDAgAEDBhzwoQMwYMCAAQMGDBgwYMCAAQMGDBgwYMCAARsbjA2AAQMGDBiwAAswYMCAAQMGDPgtcN2uhQQYMGDAgAEDBgwYMGDAgAEDBgwYMGDAgAEDBgwYMGDAgAEDBuxzSgEWYMACLMACLMACLMCABViABViABVg3HWD39G8mgeiqAAAAAElFTkSuQmCC'), (c) => c.charCodeAt(0))

export function addEvent(taskId: number, type: string, payload: Record<string, unknown>, runner = false, createdAt?: string): ApiEvent {
  const list = db.events.get(taskId) ?? []
  const runs = db.runs.get(taskId) ?? []
  const run = runs[runs.length - 1]
  const seq = runner ? list.filter((e) => e.seq !== null).length + 1 : null
  const at = createdAt ?? new Date().toISOString()
  const event = {
    id: db.nextEventId++,
    task_id: taskId,
    run_id: run?.id ?? null,
    seq,
    type,
    payload: runner ? { type, ...payload, ts: at, seq } : payload,
    created_at: at,
  } as unknown as ApiEvent
  list.push(event)
  db.events.set(taskId, list)
  track(taskId, run, type, payload, at)
  return event
}

/** Keeps the denormalised fields the backend maintains: turns, last_activity, run cost. */
function track(taskId: number, run: Run | undefined, type: string, payload: Record<string, unknown>, at: string) {
  const task = db.tasks.find((t) => t.id === taskId)
  if (!task) return
  if (type === 'tool_use') Object.assign(task, { last_activity: String(payload.summary), last_activity_at: at })
  if (type === 'status') {
    const line = payload.reason ? `${payload.status}: ${payload.reason}` : String(payload.status)
    Object.assign(task, { last_activity: line, last_activity_at: at })
  }
  if (type === 'usage' && run) {
    const runs = db.runs.get(taskId) ?? []
    const earlier = runs.filter((r) => r.id < run.id).reduce((sum, r) => sum + r.cost_usd, 0)
    run.turns = Number(payload.turns)
    run.cost_usd = Math.max(0, Number(payload.cost_usd_total) - earlier)
    task.turns = runs.reduce((sum, r) => sum + r.turns, 0)
  }
}

export function startRun(taskId: number, kind: Run['kind'], input: string, at?: string): Run {
  const run: Run = {
    id: db.nextRunId++,
    task_id: taskId,
    kind,
    input,
    container_id: null,
    exit_code: null,
    last_seq: 0,
    cost_usd: 0,
    turns: 0,
    started_at: at ?? new Date().toISOString(),
    finished_at: null,
  }
  db.runs.set(taskId, [...(db.runs.get(taskId) ?? []), run])
  addEvent(taskId, 'run_started', { run_id: run.id, kind }, false, at)
  return run
}

export function makeTask(fields: Partial<Task> & Pick<Task, 'project_id' | 'title' | 'status'>): Task {
  const proj = db.projects.find((p) => p.id === fields.project_id)
  const id = fields.id ?? db.nextTaskId++
  const slug = fields.title.toLowerCase().replace(/[^a-z0-9]+/g, '-').slice(0, 40).replace(/-$/, '')
  const task: Task = {
    id,
    repository_id: proj?.repositories.find((r) => r.is_default)?.id ?? 1,
    prompt: fields.title,
    status_reason: null,
    branch: `agent/${id}-${slug}`,
    session_id: null,
    pr_number: null,
    pr_url: null,
    ci_status: null,
    cost_usd_total: 0,
    turns: 0,
    last_activity: null,
    last_activity_at: null,
    queued_message: null,
    source: 'web',
    agent: (proj?.agent ?? MOCK_AGENTS.default_agent) as Agent,
    node: proj?.node ?? null,
    created_at: new Date().toISOString(),
    updated_at: new Date().toISOString(),
    finished_at: null,
    paused_at: null,
    ...fields,
  }
  db.tasks.unshift(task)
  return task
}

export function taskView(task: Task): TaskView {
  const runs = db.runs.get(task.id) ?? []
  const open = (db.questions.get(task.id) ?? []).find((q) => !q.answer) ?? null
  return {
    ...task,
    repository: db.projects.flatMap((p) => p.repositories).find((r) => r.id === task.repository_id) ?? null,
    latest_run: runs[runs.length - 1] ?? null,
    open_question: task.status === 'waiting_input' || task.status === 'paused' ? open : null,
  }
}

/**
 * Applies a status change like the backend's setStatusTx, including the queued
 * message rule: when the run ends in paused/pr_open/completed the queued text
 * starts the next run (status queued); failed/cancelled drop it.
 */
export function setStatus(task: Task, status: TaskStatus, reason?: string) {
  task.status = status
  task.status_reason = reason ?? null
  task.updated_at = new Date().toISOString()
  task.finished_at = ['completed', 'failed', 'cancelled'].includes(status) ? task.updated_at : null
  addEvent(task.id, 'status', reason ? { status, reason } : { status })
  // Like the backend, a completed task's images are removed (simplified: kept
  // while a queued message waits, since it may carry some of them).
  if (status === 'completed' && !task.queued_message) {
    for (const a of db.attachments.values()) if (a.taskId === task.id) a.deleted = true
  }
  const text = task.queued_message
  if (!text) return
  if (status === 'paused' || status === 'pr_open' || status === 'completed') {
    task.queued_message = null
    pendingRuns.set(task.id, { kind: status === 'paused' ? 'resume' : 'followup', input: text })
    setStatus(task, 'queued')
  } else if (status === 'failed' || status === 'cancelled') {
    task.queued_message = null
    addEvent(task.id, 'message_dropped', { text, reason: status })
  }
}

/** Runs queued by setStatus; the simulator starts them when it picks the task up. */
export const pendingRuns = new Map<number, { kind: Run['kind']; input: string }>()

/** A registered computer for mock mode (Settings → Computers, the canvas). */
export function node(id: string, name: string, extra: Partial<HostNode> = {}): HostNode {
  return {
    id,
    name,
    max_concurrent: 2,
    agents: ['claude', 'codex', 'cursor'],
    platform: 'darwin/arm64',
    version: 'dev',
    last_seen_at: ago(1),
    created_at: ago(60 * 24 * 3),
    updated_at: ago(60 * 24 * 3),
    online: true,
    active_tasks: 0,
    ...extra,
  }
}

export function seed() {
  db.nodes = [node('macbook', 'MacBook Pro')]
  db.projects = [
    project(
      'vatary',
      'Vatary',
      [repo(1, 'vatary', 'andrewsl105/vatary-server', true), repo(2, 'vatary', 'andrewsl105/vatary-web', false)],
      { services: ['postgres', 'redis'], env: { NODE_ENV: 'test' }, instructions: 'Use pnpm. Run the tests before committing.', max_turns: 80 },
    ),
    project('fleetpocket', 'Fleetpocket', [repo(3, 'fleetpocket', 'andrewsl105/fleetpocket-server', true, 'main')], { model: 'claude-opus-5-5' }),
    project('bioma', 'Bioma', [repo(4, 'bioma', 'andrewsl105/bioma', true)]),
  ]

  // A finished task with a PR.
  const t1 = makeTask({ project_id: 'vatary', title: 'Add CSV export to Tables', status: 'queued', created_at: ago(180), cost_usd_total: 1.84 })
  startRun(t1.id, 'initial', t1.prompt, ago(178))
  addEvent(t1.id, 'started', { session_id: 's-1', model: 'claude-opus-5-5' }, true, ago(178))
  addEvent(t1.id, 'assistant_text', { text: "I'll add a CSV export action to the Tables toolbar. First, let me look at how tables are rendered." }, true, ago(177))
  addEvent(t1.id, 'tool_use', { tool: 'Read', summary: 'Read src/tables/TableView.tsx' }, true, ago(177))
  addEvent(t1.id, 'tool_result', { tool: 'Read', ok: true }, true, ago(177))
  addEvent(t1.id, 'tool_use', { tool: 'Edit', summary: 'Edit src/tables/export.ts' }, true, ago(170))
  addEvent(t1.id, 'tool_result', { tool: 'Edit', ok: true }, true, ago(170))
  addEvent(t1.id, 'tool_use', { tool: 'Bash', summary: 'Bash pnpm test tables' }, true, ago(165))
  addEvent(t1.id, 'tool_result', { tool: 'Bash', ok: true }, true, ago(164))
  addEvent(t1.id, 'usage', { cost_usd_total: 1.84, input_tokens: 182000, output_tokens: 9400, turns: 23 }, true, ago(160))
  addEvent(t1.id, 'committed', { sha: 'a91c3f0e2b', files_changed: 4, insertions: 186, deletions: 12 }, true, ago(160))
  addEvent(t1.id, 'done', { session_id: 's-1', summary: 'Added **Export CSV** to the Tables toolbar:\n\n- `exportCsv()` streams rows with proper quoting\n- new button in `TableToolbar`\n- unit tests for escaping', no_changes: false, cost_usd_total: 1.84 }, true, ago(160))
  addEvent(t1.id, 'run_finished', { run_id: 1, exit_code: 0 }, false, ago(159))
  Object.assign(t1, { status: 'pr_open', pr_number: 41, pr_url: 'https://github.com/andrewsl105/vatary-server/pull/41', ci_status: 'success', updated_at: ago(150) })
  addEvent(t1.id, 'pr_opened', { number: 41, url: t1.pr_url }, false, ago(158))
  addEvent(t1.id, 'ci', { status: 'success', name: 'ci / test', url: 'https://github.com/andrewsl105/vatary-server/actions' }, false, ago(150))
  Object.assign(db.runs.get(t1.id)![0], { exit_code: 0, finished_at: ago(159), cost_usd: 1.84 })

  // Failed and completed tasks for the other filters.
  const t2 = makeTask({ project_id: 'bioma', title: 'Upgrade to React 19', status: 'failed', status_reason: 'limit_exceeded: max turns reached', created_at: ago(60 * 26), updated_at: ago(60 * 25), cost_usd_total: 3.1 })
  startRun(t2.id, 'initial', t2.prompt, ago(60 * 26))
  addEvent(t2.id, 'error', { code: 'limit_exceeded', message: 'Reached max_turns (80).' }, true, ago(60 * 25))
  addEvent(t2.id, 'run_finished', { run_id: 2, exit_code: 4 }, false, ago(60 * 25))
  makeTask({ project_id: 'fleetpocket', title: 'Document the runner protocol', status: 'completed', created_at: ago(60 * 50), updated_at: ago(60 * 48), finished_at: ago(60 * 48), cost_usd_total: 0.62, pr_number: 7, pr_url: 'https://github.com/andrewsl105/fleetpocket-server/pull/7' })
  makeTask({ project_id: 'vatary', title: 'Remove the legacy importer', status: 'cancelled', created_at: ago(60 * 72), updated_at: ago(60 * 71), cost_usd_total: 0.2 })

  // A task waiting for an answer.
  const t5 = makeTask({ project_id: 'fleetpocket', title: 'Retry webhook deliveries with backoff', status: 'queued', created_at: ago(25), source: 'telegram' })
  addEvent(t5.id, 'created', { attachments: [addAttachment(t5.id, 'retry-flow.png', 'image/png', DEMO_PNG, 160, 100)] }, false, ago(25))
  const r5 = startRun(t5.id, 'initial', t5.prompt, ago(24))
  addEvent(t5.id, 'assistant_text', { text: 'Webhook deliveries currently fail hard on the first 5xx. I can add retries in `internal/webhooks`.' }, true, ago(23))
  addEvent(t5.id, 'tool_use', { tool: 'Grep', summary: 'Grep "deliver(" internal/' }, true, ago(23))
  addEvent(t5.id, 'tool_result', { tool: 'Grep', ok: true }, true, ago(23))
  addEvent(t5.id, 'usage', { cost_usd_total: 0.38, input_tokens: 41000, output_tokens: 2100, turns: 6 }, true, ago(22))
  addEvent(t5.id, 'question', { question_id: 'q1', text: 'How many attempts should a delivery get before it is marked as failed?', options: ['3 attempts', '5 attempts', '10 attempts'] }, true, ago(21))
  db.questions.set(t5.id, [{ id: `${r5.id}-q1`, task_id: t5.id, run_id: r5.id, text: 'How many attempts should a delivery get before it is marked as failed?', options: ['3 attempts', '5 attempts', '10 attempts'], answer: null, answered_via: null, created_at: ago(21), answered_at: null }])
  Object.assign(t5, { status: 'waiting_input', cost_usd_total: 0.38, updated_at: ago(21) })

  // A long finished task: its timeline needs several pages (scroll-up paging).
  const t7 = makeTask({ project_id: 'bioma', title: 'Migrate every component to the new design tokens', status: 'queued', created_at: ago(60 * 30) })
  // Its image was removed when it completed: the timeline shows "Image removed".
  const removed = addAttachment(t7.id, 'tokens.png', 'image/png', DEMO_PNG, 160, 100)
  db.attachments.get(removed.id)!.deleted = true
  addEvent(t7.id, 'created', { attachments: [removed] }, false, ago(60 * 30))
  startRun(t7.id, 'initial', t7.prompt, ago(60 * 30))
  for (let i = 1; i <= 260; i++) {
    const file = `src/components/C${String(i).padStart(3, '0')}.tsx`
    addEvent(t7.id, 'tool_use', { tool: 'Edit', summary: `Edit ${file}` }, true, ago(60 * 30 - i / 10))
    addEvent(t7.id, 'tool_result', { tool: 'Edit', ok: true }, true, ago(60 * 30 - i / 10))
  }
  addEvent(t7.id, 'usage', { cost_usd_total: 4.2, input_tokens: 900000, output_tokens: 61000, turns: 140 }, true, ago(60 * 3))
  addEvent(t7.id, 'done', { session_id: 's-7', summary: 'Moved 260 components to the design tokens.', no_changes: false, cost_usd_total: 4.2 }, true, ago(60 * 3))
  Object.assign(db.runs.get(t7.id)![0], { exit_code: 0, finished_at: ago(60 * 3) })
  setStatus(t7, 'completed', 'committed locally')
  Object.assign(t7, { cost_usd_total: 4.2, finished_at: ago(60 * 3), updated_at: ago(60 * 3) })

  // A running task the simulator keeps alive.
  const t6 = makeTask({ project_id: 'vatary', title: 'Paginate the audit log API', status: 'queued', created_at: ago(4) })
  startRun(t6.id, 'initial', t6.prompt, ago(4))
  addEvent(t6.id, 'started', { session_id: 's-6', model: 'claude-opus-5-5' }, true, ago(4))
  addEvent(t6.id, 'assistant_text', { text: "Let me check how the audit log endpoint queries the database." }, true, ago(3))
  addEvent(t6.id, 'tool_use', { tool: 'Read', summary: 'Read src/audit/routes.ts' }, true, ago(3))
  addEvent(t6.id, 'tool_result', { tool: 'Read', ok: true }, true, ago(3))
  Object.assign(t6, { status: 'running', cost_usd_total: 0.12, updated_at: ago(2) })

  // A paused task: its question was not answered in time.
  const t8 = makeTask({ project_id: 'vatary', title: 'Rate-limit the public search endpoint', status: 'queued', created_at: ago(60 * 5), source: 'telegram' })
  const r8 = startRun(t8.id, 'initial', t8.prompt, ago(60 * 5))
  addEvent(t8.id, 'started', { session_id: 's-8', model: 'claude-opus-5-5' }, true, ago(60 * 5))
  addEvent(t8.id, 'tool_use', { tool: 'Read', summary: 'Read src/search/routes.ts' }, true, ago(60 * 5 - 1))
  addEvent(t8.id, 'tool_result', { tool: 'Read', ok: true }, true, ago(60 * 5 - 1))
  addEvent(t8.id, 'usage', { cost_usd_total: 0.27, input_tokens: 30500, output_tokens: 1400, turns: 4 }, true, ago(60 * 5 - 2))
  addEvent(t8.id, 'question', { question_id: 'q1', text: 'Should the limit apply per IP address or per API key?', options: ['Per IP address', 'Per API key'] }, true, ago(60 * 5 - 2))
  db.questions.set(t8.id, [{ id: `${r8.id}-q1`, task_id: t8.id, run_id: r8.id, text: 'Should the limit apply per IP address or per API key?', options: ['Per IP address', 'Per API key'], answer: null, answered_via: null, created_at: ago(60 * 5 - 2), answered_at: null }])
  setStatus(t8, 'paused', 'no answer within the question timeout')
  Object.assign(t8, { cost_usd_total: 0.27, paused_at: ago(60 * 2), updated_at: ago(60 * 2), last_activity_at: ago(60 * 2) })

  // A task running on the MacBook (a node), with Codex.
  const t9 = makeTask({ project_id: 'bioma', title: 'Speed up the image pipeline', status: 'queued', created_at: ago(9), node: 'macbook', agent: 'codex' })
  startRun(t9.id, 'initial', t9.prompt, ago(9))
  addEvent(t9.id, 'started', { session_id: 's-9', model: 'gpt-5.5-codex' }, true, ago(9))
  addEvent(t9.id, 'assistant_text', { text: 'Profiling the resize step first.' }, true, ago(8))
  addEvent(t9.id, 'tool_use', { tool: 'Bash', summary: 'Bash pnpm bench images' }, true, ago(7))
  addEvent(t9.id, 'tool_result', { tool: 'Bash', ok: true }, true, ago(6))
  addEvent(t9.id, 'tool_use', { tool: 'Edit', summary: 'Edit src/images/resize.ts' }, true, ago(5))
  Object.assign(t9, { status: 'running', updated_at: ago(5) })
  db.nodes[0].active_tasks = 1
}

seed()

export function resetDb() {
  db.tasks = []
  db.events.clear()
  db.runs.clear()
  db.questions.clear()
  db.attachments.clear()
  nextAttachmentId = 1
  db.nextEventId = 1
  db.nextTaskId = 1
  db.nextRunId = 1
  seed()
}
