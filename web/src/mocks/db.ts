import type { ApiEvent, Project, Question, Repository, Run, Task, TaskStatus, TaskView } from '@/api/types'

// In-memory backend for mock mode (VITE_MOCK=1) and tests. Shapes follow
// docs/backend/openapi.yaml; the OpenAPI file has no examples to generate from.

const now = Date.now()
const ago = (min: number) => new Date(now - min * 60_000).toISOString()

function repo(id: number, project_id: string, full_name: string, is_default: boolean, base_branch = 'dev'): Repository {
  return { id, project_id, full_name, base_branch, installation_id: 123, is_default }
}

function project(id: string, name: string, repositories: Repository[], extra: Partial<Project> = {}): Project {
  return {
    id,
    name,
    instructions: '',
    services: [],
    env: {},
    runner_image: null,
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
  tasks: Task[]
  events: Map<number, ApiEvent[]>
  runs: Map<number, Run[]>
  questions: Map<number, Question[]>
  nextEventId: number
  nextTaskId: number
  nextRepoId: number
  nextRunId: number
}

export const db: Db = {
  projects: [],
  tasks: [],
  events: new Map(),
  runs: new Map(),
  questions: new Map(),
  nextEventId: 1,
  nextTaskId: 1,
  nextRepoId: 10,
  nextRunId: 1,
}

export function addEvent(taskId: number, type: string, payload: Record<string, unknown>, runner = false, createdAt?: string): ApiEvent {
  const list = db.events.get(taskId) ?? []
  const runs = db.runs.get(taskId) ?? []
  const run = runs[runs.length - 1]
  const seq = runner ? list.filter((e) => e.seq !== null).length + 1 : null
  const at = createdAt ?? new Date().toISOString()
  const event: ApiEvent = {
    id: db.nextEventId++,
    task_id: taskId,
    run_id: run?.id ?? null,
    seq,
    type,
    payload: (runner ? { type, ...payload, ts: at, seq } : payload) as ApiEvent['payload'],
    created_at: at,
  }
  list.push(event)
  db.events.set(taskId, list)
  return event
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
    source: 'web',
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

export function setStatus(task: Task, status: TaskStatus, reason?: string) {
  task.status = status
  task.status_reason = reason ?? null
  task.updated_at = new Date().toISOString()
  if (['completed', 'failed', 'cancelled'].includes(status)) task.finished_at = task.updated_at
  addEvent(task.id, 'status', reason ? { status, reason } : { status })
}

export function seed() {
  db.projects = [
    project(
      'vatary',
      'Vatary',
      [repo(1, 'vatary', 'andrewsl105/vatary-server', true), repo(2, 'vatary', 'andrewsl105/vatary-web', false)],
      { services: ['postgres', 'redis'], env: { NODE_ENV: 'test' }, instructions: 'Use pnpm. Run the tests before committing.', max_turns: 80 },
    ),
    project('anora', 'Anora Runner', [repo(3, 'anora', 'andrewsl105/anora-runner', true, 'main')], { model: 'claude-opus-5-5' }),
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
  makeTask({ project_id: 'anora', title: 'Document the runner protocol', status: 'completed', created_at: ago(60 * 50), updated_at: ago(60 * 48), finished_at: ago(60 * 48), cost_usd_total: 0.62, pr_number: 7, pr_url: 'https://github.com/andrewsl105/anora-runner/pull/7' })
  makeTask({ project_id: 'vatary', title: 'Remove the legacy importer', status: 'cancelled', created_at: ago(60 * 72), updated_at: ago(60 * 71), cost_usd_total: 0.2 })

  // A task waiting for an answer.
  const t5 = makeTask({ project_id: 'anora', title: 'Retry webhook deliveries with backoff', status: 'queued', created_at: ago(25), source: 'telegram' })
  const r5 = startRun(t5.id, 'initial', t5.prompt, ago(24))
  addEvent(t5.id, 'assistant_text', { text: 'Webhook deliveries currently fail hard on the first 5xx. I can add retries in `internal/webhooks`.' }, true, ago(23))
  addEvent(t5.id, 'tool_use', { tool: 'Grep', summary: 'Grep "deliver(" internal/' }, true, ago(23))
  addEvent(t5.id, 'tool_result', { tool: 'Grep', ok: true }, true, ago(23))
  addEvent(t5.id, 'usage', { cost_usd_total: 0.38, input_tokens: 41000, output_tokens: 2100, turns: 6 }, true, ago(22))
  addEvent(t5.id, 'question', { question_id: 'q1', text: 'How many attempts should a delivery get before it is marked as failed?', options: ['3 attempts', '5 attempts', '10 attempts'] }, true, ago(21))
  db.questions.set(t5.id, [{ id: `${r5.id}-q1`, task_id: t5.id, run_id: r5.id, text: 'How many attempts should a delivery get before it is marked as failed?', options: ['3 attempts', '5 attempts', '10 attempts'], answer: null, answered_via: null, created_at: ago(21), answered_at: null }])
  Object.assign(t5, { status: 'waiting_input', cost_usd_total: 0.38, updated_at: ago(21) })

  // A running task the simulator keeps alive.
  const t6 = makeTask({ project_id: 'vatary', title: 'Paginate the audit log API', status: 'queued', created_at: ago(4) })
  startRun(t6.id, 'initial', t6.prompt, ago(4))
  addEvent(t6.id, 'started', { session_id: 's-6', model: 'claude-opus-5-5' }, true, ago(4))
  addEvent(t6.id, 'assistant_text', { text: "Let me check how the audit log endpoint queries the database." }, true, ago(3))
  addEvent(t6.id, 'tool_use', { tool: 'Read', summary: 'Read src/audit/routes.ts' }, true, ago(3))
  addEvent(t6.id, 'tool_result', { tool: 'Read', ok: true }, true, ago(3))
  Object.assign(t6, { status: 'running', cost_usd_total: 0.12, updated_at: ago(2) })
}

seed()

export function resetDb() {
  db.tasks = []
  db.events.clear()
  db.runs.clear()
  db.questions.clear()
  db.nextEventId = 1
  db.nextTaskId = 1
  db.nextRunId = 1
  seed()
}
