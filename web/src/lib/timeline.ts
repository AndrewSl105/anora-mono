import type { ApiEvent, TaskStatus } from '@/api/types'
import { fullQuestionId } from '@/api/events'

export type TimelineItem =
  | { kind: 'claude'; key: string; text: string; at: string }
  | {
      kind: 'user'
      key: string
      text: string
      at: string
      /** queued: waiting for the run to end; dropped: the task ended first; injected: legacy timelines. */
      note?: 'queued' | 'injected' | { dropped: string }
      pending?: boolean
    }
  | { kind: 'tool'; key: string; tool: string; summary: string; ok: boolean | null; error?: string; at: string }
  | {
      kind: 'question'
      key: string
      questionId: string
      text: string
      options: string[]
      answer?: { text: string; via: string; pending?: boolean }
      at: string
    }
  | { kind: 'divider'; key: string; text: string; tone?: 'muted' | 'warn'; at: string }
  | { kind: 'status'; key: string; status: TaskStatus; reason?: string | null; at: string }
  | { kind: 'commit'; key: string; sha: string; files: number; insertions: number; deletions: number; at: string }
  | { kind: 'pr'; key: string; action: 'opened' | 'updated' | 'closed'; number: number; url?: string; merged?: boolean; at: string }
  | { kind: 'ci'; key: string; status: string; name: string; url?: string; at: string }
  | { kind: 'done'; key: string; summary: string; noChanges: boolean; at: string }
  | { kind: 'error'; key: string; title: string; message: string; at: string }

/**
 * Interrupt ends the run (the backend pushes what it committed): a task whose
 * current run already got an interrupt is stopping until the run finishes.
 */
export function isStopping(events: ApiEvent[], status: TaskStatus): boolean {
  if (status !== 'running' && status !== 'waiting_input') return false
  for (let i = events.length - 1; i >= 0; i--) {
    const type = events[i].type
    if (type === 'interrupt') return true
    if (type === 'run_started' || type === 'run_finished') return false
  }
  return false
}

/**
 * Maps raw events (real, then optimistic local ones) to timeline items. Pure.
 * Turns, the latest activity and the runs list come from the API (Task.turns,
 * Task.last_activity, GET /tasks/{id}/runs), not from the loaded events.
 */
export function buildTimeline(events: ApiEvent[], local: ApiEvent[] = []): TimelineItem[] {
  const items: TimelineItem[] = []
  const questions = new Map<string, Extract<TimelineItem, { kind: 'question' }>>()
  const openTools = new Map<string, Extract<TimelineItem, { kind: 'tool' }>>()
  // Queued messages wait for the run to end: the move to `queued` starts them
  // as the next run; `message_dropped` means the task ended first.
  let waiting: Extract<TimelineItem, { kind: 'user' }>[] = []

  const all = [...events, ...local]
  for (const e of all) {
    const key = String(e.id)
    const at = e.created_at
    const pending = e.id < 0
    switch (e.type) {
      case 'assistant_text': {
        const text = e.payload.text
        if (text?.trim()) items.push({ kind: 'claude', key, text, at })
        break
      }
      case 'tool_use': {
        const { tool, summary } = e.payload
        const item: Extract<TimelineItem, { kind: 'tool' }> = { kind: 'tool', key, tool, summary: summary || tool, ok: null, at }
        items.push(item)
        openTools.set(tool, item)
        break
      }
      case 'tool_result': {
        const { tool, ok, summary } = e.payload
        const item = openTools.get(tool)
        if (item) {
          item.ok = ok
          if (!ok && summary) item.error = summary
          openTools.delete(tool)
        }
        break
      }
      case 'question': {
        const q = e.payload
        const questionId = fullQuestionId(e.run_id, q.question_id)
        const item: Extract<TimelineItem, { kind: 'question' }> = {
          kind: 'question',
          key,
          questionId,
          text: q.text,
          options: q.options ?? [],
          at,
        }
        items.push(item)
        questions.set(questionId, item)
        break
      }
      case 'answer': {
        const a = e.payload
        const q = questions.get(a.question_id) ?? [...questions.values()].find((x) => x.questionId.endsWith(`-${a.question_id}`))
        if (q) {
          // A real answer replaces a pending optimistic one.
          if (!q.answer || q.answer.pending) q.answer = { text: a.text, via: a.answered_via, pending }
        } else {
          items.push({ kind: 'user', key, text: a.text, at, pending })
        }
        break
      }
      case 'message': {
        const m = e.payload
        const item: Extract<TimelineItem, { kind: 'user' }> = { kind: 'user', key, text: m.text, at, pending }
        if (m.delivery === 'queued') {
          item.note = 'queued'
          waiting.push(item)
        } else if (m.delivery === 'injected') item.note = 'injected'
        items.push(item)
        break
      }
      case 'message_dropped':
        for (const w of waiting) w.note = { dropped: e.payload.reason }
        waiting = []
        break
      case 'status': {
        const s = e.payload
        if (s.status === 'queued') {
          for (const w of waiting) if (!w.pending) w.note = undefined
          waiting = waiting.filter((w) => w.pending)
        }
        items.push({ kind: 'status', key, status: s.status as TaskStatus, reason: s.reason, at })
        break
      }
      case 'run_started': {
        const r = e.payload
        items.push({ kind: 'divider', key, text: `run_started:${r.kind}`, at })
        break
      }
      case 'run_finished': {
        const r = e.payload
        openTools.clear()
        items.push({ kind: 'divider', key, text: `run_finished:${r.exit_code ?? ''}`, at })
        break
      }
      case 'interrupt':
        items.push({ kind: 'divider', key, text: 'interrupt', at })
        break
      case 'paused':
        items.push({ kind: 'divider', key, text: 'paused', tone: 'warn', at })
        break
      case 'committed': {
        const c = e.payload
        items.push({ kind: 'commit', key, sha: c.sha, files: c.files_changed, insertions: c.insertions, deletions: c.deletions, at })
        break
      }
      case 'pr_opened':
      case 'pr_updated': {
        const pr = e.payload
        items.push({ kind: 'pr', key, action: e.type === 'pr_opened' ? 'opened' : 'updated', number: pr.number, url: pr.url, at })
        break
      }
      case 'pr_closed': {
        const pr = e.payload
        items.push({ kind: 'pr', key, action: 'closed', number: pr.number, merged: pr.merged, at })
        break
      }
      case 'ci': {
        const c = e.payload
        items.push({ kind: 'ci', key, status: c.status, name: c.name, url: c.url, at })
        break
      }
      case 'done': {
        const d = e.payload
        items.push({ kind: 'done', key, summary: d.summary, noChanges: d.no_changes, at })
        break
      }
      case 'error': {
        const er = e.payload
        items.push({ kind: 'error', key, title: er.code, message: er.message, at })
        break
      }
      case 'diff_rejected':
        items.push({ kind: 'error', key, title: 'diff_rejected', message: e.payload.reason, at })
        break
      default:
        // ready, started, heartbeat and unknown types are not shown.
        break
    }
  }

  return items
}
