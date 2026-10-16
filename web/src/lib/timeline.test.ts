import { describe, expect, it } from 'vitest'
import type { ApiEvent } from '@/api/types'
import { buildTimeline, isStopping } from './timeline'
import { composerMode } from './status'

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

  it('labels queued messages until the next run starts, or as not sent', () => {
    const delivered = buildTimeline([
      ev('message', { text: 'one', delivery: 'queued' }, { runner: false }),
      ev('status', { status: 'pr_open' }, { runner: false }),
      ev('status', { status: 'queued' }, { runner: false }),
      ev('message', { text: 'two', delivery: 'queued' }, { runner: false }),
    ])
    expect(delivered.filter((i) => i.kind === 'user').map((i) => i.kind === 'user' && i.note)).toEqual([undefined, 'queued'])

    const dropped = buildTimeline([
      ev('message', { text: 'late', delivery: 'queued' }, { runner: false }),
      ev('status', { status: 'cancelled' }, { runner: false }),
      ev('message_dropped', { text: 'late', reason: 'cancelled' }, { runner: false }),
    ])
    expect(dropped[0]).toMatchObject({ kind: 'user', note: { dropped: 'cancelled' } })

    const legacy = buildTimeline([ev('message', { text: 'old', delivery: 'injected' }, { runner: false })])
    expect(legacy[0]).toMatchObject({ note: 'injected' })
  })

  it('is stopping between an interrupt and the end of the run', () => {
    const started = ev('run_started', { run_id: 3, kind: 'initial' }, { runner: false })
    const interrupt = ev('interrupt', {}, { runner: false })
    expect(isStopping([started], 'running')).toBe(false)
    expect(isStopping([started, interrupt], 'running')).toBe(true)
    expect(isStopping([started, interrupt], 'pushing')).toBe(false)
    expect(isStopping([interrupt, ev('run_started', { run_id: 4, kind: 'followup' }, { runner: false })], 'running')).toBe(false)
  })
})

describe('composerMode', () => {
  it('queues while a run is pending or active and starts a run otherwise', () => {
    expect(['queued', 'preparing', 'running', 'waiting_input', 'pushing'].map((s) => composerMode(s as never))).toEqual(Array(5).fill('queue'))
    expect(['paused', 'pr_open', 'completed'].map((s) => composerMode(s as never))).toEqual(Array(3).fill('followup'))
    expect(composerMode('failed')).toBeNull()
    expect(composerMode('cancelled')).toBeNull()
  })
})

describe('attachments', () => {
  const att = { id: 5, name: 'mock.png', mime: 'image/png' as const, size: 100, width: 800, height: 600 }
  const ev = (id: number, type: string, payload: unknown) =>
    ({ id, task_id: 1, run_id: null, seq: null, type, payload, created_at: '2026-10-16T10:00:00Z' }) as unknown as ApiEvent

  it('shows the task images from its created event', () => {
    const items = buildTimeline([ev(1, 'created', { attachments: [att] })])
    expect(items).toEqual([{ kind: 'attachments', key: '1', attachments: [att], at: '2026-10-16T10:00:00Z' }])
  })

  it('shows nothing for a created event without images', () => {
    expect(buildTimeline([ev(1, 'created', { attachments: [] })])).toEqual([])
  })

  it('puts message images in the bubble', () => {
    const [item] = buildTimeline([ev(2, 'message', { text: 'like this', delivery: 'new_run', attachments: [att] })])
    expect(item).toMatchObject({ kind: 'user', text: 'like this', attachments: [att] })
    const [plain] = buildTimeline([ev(3, 'message', { text: 'no images', delivery: 'new_run' })])
    expect(plain).not.toHaveProperty('attachments')
  })
})
