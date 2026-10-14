import { describe, expect, it } from 'vitest'
import type { ApiEvent } from '@/api/types'
import { buildTimeline } from './timeline'

let id = 0
function ev(type: string, payload: Record<string, unknown>, opts: { run?: number; runner?: boolean; id?: number } = {}): ApiEvent {
  const eid = opts.id ?? ++id
  return {
    id: eid,
    task_id: 1,
    run_id: opts.run ?? 3,
    seq: opts.runner === false ? null : eid,
    type,
    payload: { type, ...payload },
    created_at: '2026-10-14T10:00:00Z',
  } as unknown as ApiEvent
}

describe('buildTimeline', () => {
  it('maps messages, tools and dividers', () => {
    const items = buildTimeline([
      ev('run_started', { run_id: 3, kind: 'initial' }, { runner: false }),
      ev('started', { session_id: 's', model: 'm' }),
      ev('assistant_text', { text: 'Hello' }),
      ev('tool_use', { tool: 'Edit', summary: 'Edit src/a.ts' }),
      ev('tool_result', { tool: 'Edit', ok: true }),
      ev('heartbeat', {}),
    ])
    expect(items.map((i) => i.kind)).toEqual(['divider', 'claude', 'tool'])
    expect(items[2]).toMatchObject({ kind: 'tool', summary: 'Edit src/a.ts', ok: true })
  })

  it('marks a failed tool with its error summary', () => {
    const items = buildTimeline([
      ev('tool_use', { tool: 'Bash', summary: 'Bash npm test' }),
      ev('tool_result', { tool: 'Bash', ok: false, summary: 'FAIL x.test.ts' }),
    ])
    expect(items[0]).toMatchObject({ ok: false, error: 'FAIL x.test.ts' })
  })

  it('attaches an answer to its question by full id', () => {
    const items = buildTimeline([
      ev('question', { question_id: 'q1', text: 'Which?', options: ['a', 'b'] }, { run: 7 }),
      ev('answer', { question_id: '7-q1', text: 'a', answered_via: 'telegram' }, { runner: false }),
    ])
    expect(items).toHaveLength(1)
    expect(items[0]).toMatchObject({ kind: 'question', questionId: '7-q1', options: ['a', 'b'], answer: { text: 'a', via: 'telegram' } })
  })

  it('shows optimistic local messages as pending', () => {
    const local = ev('message', { text: 'hi', delivery: 'queued' }, { id: -1 })
    const items = buildTimeline([], [local])
    expect(items[0]).toMatchObject({ kind: 'user', text: 'hi', pending: true })
  })

  it('renders status, commit, PR, CI, done and errors', () => {
    const items = buildTimeline([
      ev('status', { status: 'pushing' }, { runner: false }),
      ev('committed', { sha: 'abc', files_changed: 2, insertions: 5, deletions: 1 }),
      ev('done', { summary: 'ok', no_changes: false }),
      ev('pr_opened', { number: 4, url: 'u' }, { runner: false }),
      ev('ci', { status: 'failure', name: 'test', url: 'u' }, { runner: false }),
      ev('pr_closed', { number: 4, merged: true }, { runner: false }),
      ev('error', { code: 'sdk_error', message: 'boom' }),
      ev('diff_rejected', { reason: 'too big' }, { runner: false }),
    ])
    expect(items.map((i) => i.kind)).toEqual(['status', 'commit', 'done', 'pr', 'ci', 'pr', 'error', 'error'])
    expect(items[5]).toMatchObject({ action: 'closed', merged: true })
  })
})
