import { describe, expect, it } from 'vitest'
import { QueryClient } from '@tanstack/react-query'
import { keys } from './keys'
import { mergeEvents, upsertTask, type TasksData } from './cache'
import type { ApiEvent, Task } from './types'

const event = (id: number, type = 'message', text = `t${id}`): ApiEvent => ({
  id,
  task_id: 1,
  run_id: null,
  seq: null,
  type,
  payload: { text, delivery: 'injected' } as unknown as ApiEvent['payload'],
  created_at: '2026-10-14T10:00:00Z',
})

const task = (id: number, status: Task['status'], project = 'p'): Task => ({
  id,
  project_id: project,
  repository_id: 1,
  title: `T${id}`,
  prompt: '',
  status,
  cost_usd_total: 0,
  source: 'web',
  created_at: '2026-10-14T10:00:00Z',
  updated_at: '2026-10-14T10:00:00Z',
})

describe('mergeEvents', () => {
  it('drops an optimistic local event once its real echo arrives', () => {
    const merged = mergeEvents({ items: [event(1)], local: [event(-1, 'message', 'hello')] }, [event(2, 'message', 'hello')])
    expect(merged.items.map((e) => e.id)).toEqual([1, 2])
    expect(merged.local).toEqual([])
  })
})

describe('upsertTask', () => {
  it('moves a task between filtered lists as its status changes', () => {
    const qc = new QueryClient()
    const active = keys.tasks.list({ group: 'active' })
    const prOpen = keys.tasks.list({ group: 'pr_open' })
    const page = (items: Task[]): TasksData => ({ pages: [{ items, next_cursor: '' }], pageParams: [undefined] })
    qc.setQueryData(active, page([task(1, 'running'), task(2, 'queued')]))
    qc.setQueryData(prOpen, page([]))

    upsertTask(qc, task(1, 'pr_open'))
    expect(qc.getQueryData<TasksData>(active)!.pages[0].items.map((t) => t.id)).toEqual([2])
    expect(qc.getQueryData<TasksData>(prOpen)!.pages[0].items.map((t) => t.id)).toEqual([1])

    upsertTask(qc, task(3, 'queued'))
    expect(qc.getQueryData<TasksData>(active)!.pages[0].items.map((t) => t.id)).toEqual([3, 2])
  })

  it('respects the project filter', () => {
    const qc = new QueryClient()
    const key = keys.tasks.list({ group: 'active', project: 'a' })
    qc.setQueryData<TasksData>(key, { pages: [{ items: [], next_cursor: '' }], pageParams: [undefined] })
    upsertTask(qc, task(1, 'running', 'b'))
    expect(qc.getQueryData<TasksData>(key)!.pages[0].items).toEqual([])
  })
})
