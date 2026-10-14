import type { ApiEvent } from './types'

// The OpenAPI file types Event.payload as a bare object. These payload shapes
// come from docs/backend/PROTOCOL.md (runner events) and docs/backend/API.md
// (backend events); see docs/DECISIONS.md.
export interface PayloadMap {
  // runner events (seq set)
  ready: { runner_version: string }
  started: { session_id: string; model: string }
  assistant_text: { text: string }
  tool_use: { tool: string; summary: string }
  tool_result: { tool: string; ok: boolean; summary?: string }
  question: { question_id: string; text: string; options?: string[] | null }
  usage: { cost_usd_total: number; input_tokens: number; output_tokens: number; turns: number }
  committed: { sha: string; files_changed: number; insertions: number; deletions: number }
  done: { session_id: string; summary: string; no_changes: boolean; cost_usd_total: number }
  paused: { session_id: string; reason: string; question_id: string }
  error: { code: string; message: string; session_id?: string }
  // backend events (seq null)
  status: { status: string; reason?: string | null }
  answer: { question_id: string; text: string; answered_via: 'web' | 'telegram' }
  message: { text: string; delivery: 'injected' | 'new_run' }
  interrupt: Record<string, never>
  run_started: { run_id: number; kind: string }
  run_finished: { run_id: number; exit_code: number | null }
  pr_opened: { number: number; url: string }
  pr_updated: { number: number; url: string }
  pr_closed: { number: number; merged: boolean }
  ci: { status: 'pending' | 'success' | 'failure'; name: string; url: string }
  diff_rejected: { reason: string }
}

export type EventType = keyof PayloadMap

export type TypedEvent<K extends EventType = EventType> = Omit<ApiEvent, 'type' | 'payload'> & {
  type: K
  payload: PayloadMap[K]
}

export function isEvent<K extends EventType>(e: ApiEvent, type: K): e is ApiEvent & TypedEvent<K> {
  return e.type === type
}

/** Full question id as the backend stores it: "<run_id>-<runner question id>". */
export function fullQuestionId(runId: number | null | undefined, questionId: string): string {
  if (runId == null || questionId.includes('-')) return questionId
  return `${runId}-${questionId}`
}
