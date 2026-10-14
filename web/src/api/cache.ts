import type { InfiniteData, QueryClient } from '@tanstack/react-query'
import { keys, type TaskFilter } from './keys'
import type { ApiEvent, Page, Task, TaskView } from './types'
import { GROUPS } from '@/lib/status'

/** Timeline cache: real events sorted by id, plus optimistic local ones (negative ids). */
export type EventsData = { items: ApiEvent[]; local: ApiEvent[] }

export type TasksData = InfiniteData<Page<Task>, string | undefined>

export function lastEventId(data: EventsData | undefined): number {
  return data?.items.length ? data.items[data.items.length - 1].id : 0
}

/** Merges events by id (dedupe) and keeps them sorted; drops local echoes of arrived events. */
export function mergeEvents(prev: EventsData | undefined, incoming: ApiEvent[]): EventsData {
  const base = prev ?? { items: [], local: [] }
  const fresh = incoming.filter((e) => e.id > 0)
  if (fresh.length === 0) return base
  const byId = new Map<number, ApiEvent>()
  for (const e of base.items) byId.set(e.id, e)
  for (const e of fresh) byId.set(e.id, e)
  const items = [...byId.values()].sort((a, b) => a.id - b.id)
  const local = base.local.filter((l) => !fresh.some((e) => isEchoOf(e, l)))
  return { items, local }
}

function isEchoOf(real: ApiEvent, local: ApiEvent): boolean {
  if (real.type !== local.type) return false
  const a = real.payload as { text?: string; question_id?: string }
  const b = local.payload as { text?: string; question_id?: string }
  return a.text === b.text && (local.type !== 'answer' || a.question_id === b.question_id)
}

export function appendEvents(qc: QueryClient, taskId: number, events: ApiEvent[]) {
  const key = keys.tasks.events(taskId)
  // Only keep a live timeline for tasks that already have one loaded.
  if (!qc.getQueryData(key)) return
  qc.setQueryData<EventsData>(key, (prev) => mergeEvents(prev, events))
}

let localSeq = 0
export function addLocalEvent(qc: QueryClient, taskId: number, type: string, payload: Record<string, unknown>): ApiEvent {
  const event: ApiEvent = {
    id: -++localSeq,
    task_id: taskId,
    run_id: null,
    seq: null,
    type,
    payload: payload as ApiEvent['payload'],
    created_at: new Date().toISOString(),
  }
  qc.setQueryData<EventsData>(keys.tasks.events(taskId), (prev) => ({
    items: prev?.items ?? [],
    local: [...(prev?.local ?? []), event],
  }))
  return event
}

export function removeLocalEvent(qc: QueryClient, taskId: number, id: number) {
  qc.setQueryData<EventsData>(keys.tasks.events(taskId), (prev) =>
    prev ? { ...prev, local: prev.local.filter((e) => e.id !== id) } : prev,
  )
}

export function matchesFilter(task: Task, filter: TaskFilter): boolean {
  return GROUPS[filter.group].includes(task.status) && (!filter.project || filter.project === task.project_id)
}

/** A `task` push or mutation result replaces the task in every list and the detail cache. */
export function upsertTask(qc: QueryClient, task: Task) {
  for (const [key, data] of qc.getQueriesData<TasksData>({ queryKey: keys.tasks.lists() })) {
    if (!data) continue
    const filter = key[2] as TaskFilter
    const matches = matchesFilter(task, filter)
    let found = false
    const pages = data.pages.map((page) => {
      const items = page.items.flatMap((t) => {
        if (t.id !== task.id) return [t]
        found = true
        return matches ? [{ ...t, ...task }] : []
      })
      return { ...page, items }
    })
    if (!found && matches && pages.length > 0) {
      pages[0] = { ...pages[0], items: [task, ...pages[0].items] }
    }
    qc.setQueryData<TasksData>(key, { ...data, pages })
  }
  const detailKey = keys.tasks.detail(task.id)
  const prev = qc.getQueryData<TaskView>(detailKey)
  if (prev) {
    qc.setQueryData<TaskView>(detailKey, { ...prev, ...task })
    // open_question, latest_run and repository only come with the full view.
    if (prev.status !== task.status) void qc.invalidateQueries({ queryKey: detailKey, exact: true })
  }
}
