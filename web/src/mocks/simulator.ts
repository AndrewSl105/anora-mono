import type { ApiEvent, Task } from '@/api/types'
import { addEvent, db, pendingRuns, setStatus, startRun } from './db'

// Drives running tasks in mock mode so the live timeline has something to show.

type Push = { event: (taskId: number, e: ApiEvent) => void; task: (t: Task) => void }
let push: Push = { event: () => {}, task: () => {} }

export function setPush(p: Push) {
  push = p
}

function emit(taskId: number, type: string, payload: Record<string, unknown>, runner = true) {
  push.event(taskId, addEvent(taskId, type, payload, runner))
}

function emitStatus(task: Task, status: Task['status'], reason?: string) {
  const events = db.events.get(task.id) ?? []
  const before = events.length
  setStatus(task, status, reason)
  // setStatus may add more than one event (a drained queued message).
  for (const e of (db.events.get(task.id) ?? []).slice(before)) push.event(task.id, e)
  push.task(task)
}

/** Ends the current run early: done, then push like a finished run. */
function interruptRun(task: Task) {
  setTimeout(() => {
    emit(task.id, 'done', { session_id: 's', summary: 'Interrupted by the user.', no_changes: false, cost_usd_total: task.cost_usd_total })
    progress.set(task.id, SCRIPT.length + 1)
    emitStatus(task, 'pushing')
  }, 800)
}

const SCRIPT: Array<(task: Task) => void> = [
  (t) => emit(t.id, 'tool_use', { tool: 'Grep', summary: 'Grep "LIMIT" src/audit' }),
  (t) => emit(t.id, 'tool_result', { tool: 'Grep', ok: true }),
  (t) => emit(t.id, 'assistant_text', { text: 'The endpoint loads the whole table. I will switch it to **keyset pagination** on `(created_at, id)`.' }),
  (t) => emit(t.id, 'tool_use', { tool: 'Edit', summary: 'Edit src/audit/routes.ts' }),
  (t) => emit(t.id, 'tool_result', { tool: 'Edit', ok: true }),
  (t) => emit(t.id, 'tool_use', { tool: 'Edit', summary: 'Edit src/audit/repository.ts' }),
  (t) => emit(t.id, 'tool_result', { tool: 'Edit', ok: true }),
  (t) => {
    t.cost_usd_total = Math.round((t.cost_usd_total + 0.21) * 100) / 100
    emit(t.id, 'usage', { cost_usd_total: t.cost_usd_total, input_tokens: 52000, output_tokens: 3100, turns: 9 })
    push.task(t)
  },
  (t) => emit(t.id, 'tool_use', { tool: 'Bash', summary: 'Bash pnpm test audit' }),
  (t) => emit(t.id, 'tool_result', { tool: 'Bash', ok: false, summary: 'FAIL src/audit/routes.test.ts › returns next cursor' }),
  (t) => emit(t.id, 'assistant_text', { text: 'One test expects `next_cursor` to be empty on the last page. Fixing the boundary check.' }),
  (t) => emit(t.id, 'tool_use', { tool: 'Edit', summary: 'Edit src/audit/routes.ts' }),
  (t) => emit(t.id, 'tool_result', { tool: 'Edit', ok: true }),
  (t) => emit(t.id, 'tool_use', { tool: 'Bash', summary: 'Bash pnpm test audit' }),
  (t) => emit(t.id, 'tool_result', { tool: 'Bash', ok: true }),
]

const progress = new Map<number, number>()
let timer: ReturnType<typeof setInterval> | undefined

export function startSimulator(intervalMs = 2500) {
  if (timer) return
  timer = setInterval(() => {
    for (const task of db.tasks.filter((t) => t.status === 'running')) {
      const step = progress.get(task.id) ?? 0
      if (step < SCRIPT.length) {
        SCRIPT[step](task)
        progress.set(task.id, step + 1)
      } else if (step === SCRIPT.length) {
        emit(task.id, 'committed', { sha: Math.random().toString(16).slice(2, 12), files_changed: 3, insertions: 74, deletions: 19 })
        emit(task.id, 'done', { session_id: 's', summary: 'Switched the audit log to keyset pagination; tests pass.', no_changes: false, cost_usd_total: task.cost_usd_total })
        emitStatus(task, 'pushing')
        progress.set(task.id, step + 1)
      }
    }
    for (const task of db.tasks.filter((t) => t.status === 'pushing')) {
      const n = 40 + task.id
      Object.assign(task, { pr_number: n, pr_url: `https://github.com/andrewsl105/example/pull/${n}`, ci_status: 'pending' })
      emit(task.id, 'pr_opened', { number: n, url: task.pr_url }, false)
      emitStatus(task, 'pr_open')
    }
    for (const task of db.tasks.filter((t) => t.status === 'queued' || t.status === 'preparing')) {
      const next = pendingRuns.get(task.id)
      if (task.status === 'preparing' && next) {
        pendingRuns.delete(task.id)
        startRun(task.id, next.kind, next.input)
        const events = db.events.get(task.id)!
        push.event(task.id, events[events.length - 1])
        progress.set(task.id, 0)
      }
      emitStatus(task, task.status === 'queued' ? 'preparing' : 'running')
    }
  }, intervalMs)
}

export function resumeAfterAnswer(task: Task) {
  progress.set(task.id, Math.max(progress.get(task.id) ?? 0, 0))
  emitStatus(task, 'running')
}

export function stopSimulator() {
  clearInterval(timer)
  timer = undefined
  progress.clear()
}

export { emitStatus, interruptRun }
