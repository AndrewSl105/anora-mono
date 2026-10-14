import type { ApiEvent, TaskStatus } from '@/api/types'
import { fullQuestionId } from '@/api/events'

export type TimelineItem =
  | { kind: 'claude'; key: string; text: string; at: string }
  | { kind: 'user'; key: string; text: string; at: string; delivery?: string; pending?: boolean }
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

export type RunInfo = {
  runId: number
  kind: string
  startedAt: string
  finishedAt?: string
  exitCode?: number | null
  cost?: number
}

export type TimelineSummary = {
  items: TimelineItem[]
  turns: number | null
  latestTool: string | null
  runs: RunInfo[]
}

/** Maps raw events (real, then optimistic local ones) to timeline items. Pure. */
export function buildTimeline(events: ApiEvent[], local: ApiEvent[] = []): TimelineSummary {
  const items: TimelineItem[] = []
  const questions = new Map<string, Extract<TimelineItem, { kind: 'question' }>>()
  const openTools = new Map<string, Extract<TimelineItem, { kind: 'tool' }>>()
  const runs = new Map<number, RunInfo>()
  const runCostTotals = new Map<number, number>()
  let turns: number | null = null
  let latestTool: string | null = null

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
        latestTool = item.summary
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
        items.push({ kind: 'user', key, text: m.text, at, delivery: m.delivery, pending })
        break
      }
      case 'usage': {
        const u = e.payload
        if (typeof u.turns === 'number') turns = u.turns
        if (e.run_id != null && typeof u.cost_usd_total === 'number') runCostTotals.set(e.run_id, u.cost_usd_total)
        break
      }
      case 'status': {
        const s = e.payload
        items.push({ kind: 'status', key, status: s.status as TaskStatus, reason: s.reason, at })
        break
      }
      case 'run_started': {
        const r = e.payload
        runs.set(r.run_id, { runId: r.run_id, kind: r.kind, startedAt: at })
        items.push({ kind: 'divider', key, text: `run_started:${r.kind}`, at })
        break
      }
      case 'run_finished': {
        const r = e.payload
        const run = runs.get(r.run_id) ?? { runId: r.run_id, kind: '—', startedAt: at }
        run.finishedAt = at
        run.exitCode = r.exit_code
        runs.set(r.run_id, run)
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

  // Per-run cost: cumulative session total minus the previous run's last total.
  let previous = 0
  const runList = [...runs.values()].sort((a, b) => a.runId - b.runId)
  for (const run of runList) {
    const total = runCostTotals.get(run.runId)
    if (total !== undefined) {
      run.cost = Math.max(0, total - previous)
      previous = total
    }
  }

  return { items, turns, latestTool, runs: runList }
}
