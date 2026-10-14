import {
  useInfiniteQuery,
  useMutation,
  useQuery,
  useQueryClient,
  type QueryClient,
} from '@tanstack/react-query'
import { toast } from 'sonner'
import { api, call, CSRF } from './client'
import { keys, type TaskFilter } from './keys'
import {
  addLocalEvent,
  lastEventId,
  mergeEvents,
  removeLocalEvent,
  upsertTask,
  type EventsData,
} from './cache'
import type { ApiEvent, Health, Project, ProjectFields, TaskView } from './types'
import { isApiError } from './errors'
import { GROUPS } from '@/lib/status'
import { t } from '@/i18n/en'
import { haptic } from '@/telegram/haptics'

const EVENTS_PAGE = 200

export function errorMessage(e: unknown): string {
  if (isApiError(e)) {
    const known = (t.errors as Record<string, string>)[e.code]
    return known ?? e.message ?? t.errors.generic
  }
  return t.errors.generic
}

function toastError(e: unknown) {
  haptic.error()
  toast.error(errorMessage(e))
}

// ---- Tasks ----

export function useTasks(filter: TaskFilter) {
  return useInfiniteQuery({
    queryKey: keys.tasks.list(filter),
    initialPageParam: undefined as string | undefined,
    queryFn: ({ pageParam, signal }) =>
      call(
        api.GET('/api/v1/tasks', {
          params: {
            query: {
              status: GROUPS[filter.group].join(','),
              project: filter.project || undefined,
              limit: 50,
              cursor: pageParam,
            },
          },
          signal,
        }),
      ),
    getNextPageParam: (last) => last.next_cursor || undefined,
    staleTime: 30_000,
  })
}

export function useTask(id: number) {
  return useQuery({
    queryKey: keys.tasks.detail(id),
    queryFn: ({ signal }) => call(api.GET('/api/v1/tasks/{id}', { params: { path: { id } }, signal })),
    staleTime: 30_000,
    retry: (count, e) => !isApiError(e, 'not_found') && count < 2,
  })
}

/**
 * Loads the timeline forward from the last event id in the cache. The first call
 * loads everything; a refetch (e.g. after a WebSocket reconnect) only fetches
 * what was missed. See docs/DECISIONS.md for why this is not a backward pager.
 */
export async function fetchEventsIncremental(qc: QueryClient, id: number, signal?: AbortSignal): Promise<EventsData> {
  const key = keys.tasks.events(id)
  let after = lastEventId(qc.getQueryData<EventsData>(key))
  const fetched: ApiEvent[] = []
  for (let i = 0; i < 500; i++) {
    const page = await call(
      api.GET('/api/v1/tasks/{id}/events', {
        params: { path: { id }, query: { after_id: after || undefined, limit: EVENTS_PAGE } },
        signal,
      }),
    )
    fetched.push(...page.items)
    if (!page.next_cursor || page.items.length === 0) break
    after = page.items[page.items.length - 1].id
  }
  // Merge with whatever the WebSocket appended meanwhile.
  return mergeEvents(qc.getQueryData<EventsData>(key), fetched)
}

export function useTaskEvents(id: number) {
  const qc = useQueryClient()
  return useQuery({
    queryKey: keys.tasks.events(id),
    queryFn: ({ signal }) => fetchEventsIncremental(qc, id, signal),
    staleTime: Infinity,
    refetchOnWindowFocus: false,
  })
}

export function useTaskDiff(id: number, enabled: boolean) {
  return useQuery({
    queryKey: keys.tasks.diff(id),
    queryFn: ({ signal }) => call(api.GET('/api/v1/tasks/{id}/diff', { params: { path: { id } }, signal })),
    enabled,
    staleTime: 15_000,
    retry: (count, e) => !isApiError(e, 'no_workspace') && count < 2,
  })
}

function useTaskResult() {
  const qc = useQueryClient()
  return (task: TaskView) => {
    qc.setQueryData(keys.tasks.detail(task.id), task)
    upsertTask(qc, task)
  }
}

export function useCreateTask() {
  const qc = useQueryClient()
  const apply = useTaskResult()
  return useMutation({
    mutationFn: (body: { project_id: string; prompt: string; repository_id?: number; title?: string }) =>
      call(api.POST('/api/v1/tasks', { params: { header: CSRF }, body })),
    onSuccess: (task) => {
      apply(task)
      void qc.invalidateQueries({ queryKey: keys.stats })
    },
  })
}

export function useAnswer(taskId: number) {
  const qc = useQueryClient()
  const apply = useTaskResult()
  return useMutation({
    mutationFn: (body: { question_id: string; text: string }) =>
      call(api.POST('/api/v1/tasks/{id}/answer', { params: { path: { id: taskId }, header: CSRF }, body })),
    onMutate: (body) => {
      const local = addLocalEvent(qc, taskId, 'answer', { ...body, answered_via: 'web' })
      return { localId: local.id }
    },
    onSuccess: (task) => {
      haptic.success()
      apply(task)
    },
    onError: (e, _body, ctx) => {
      if (ctx) removeLocalEvent(qc, taskId, ctx.localId)
      toastError(e)
      void qc.invalidateQueries({ queryKey: keys.tasks.detail(taskId) })
    },
  })
}

export function useSendMessage(taskId: number) {
  const qc = useQueryClient()
  const apply = useTaskResult()
  return useMutation({
    mutationFn: (text: string) =>
      call(api.POST('/api/v1/tasks/{id}/message', { params: { path: { id: taskId }, header: CSRF }, body: { text } })),
    onMutate: (text) => {
      const local = addLocalEvent(qc, taskId, 'message', { text, delivery: 'injected' })
      return { localId: local.id }
    },
    onSuccess: (task) => {
      haptic.success()
      apply(task)
    },
    onError: (e, _text, ctx) => {
      if (ctx) removeLocalEvent(qc, taskId, ctx.localId)
      toastError(e)
    },
  })
}

type TaskActionKind = 'cancel' | 'interrupt' | 'retry'
const ACTION_PATH = {
  cancel: '/api/v1/tasks/{id}/cancel',
  interrupt: '/api/v1/tasks/{id}/interrupt',
  retry: '/api/v1/tasks/{id}/retry',
} as const
const ACTION_DONE = { cancel: t.task.cancelled, interrupt: t.task.interrupted, retry: t.task.retried }

export function useTaskAction(taskId: number) {
  const apply = useTaskResult()
  return useMutation({
    mutationFn: (kind: TaskActionKind) =>
      call(api.POST(ACTION_PATH[kind], { params: { path: { id: taskId }, header: CSRF } })),
    onSuccess: (task, kind) => {
      haptic.success()
      apply(task)
      toast.success(ACTION_DONE[kind])
    },
    onError: toastError,
  })
}

// ---- Projects ----

export function useProjects() {
  return useQuery({
    queryKey: keys.projects.list(),
    queryFn: async ({ signal }) => {
      const items: Project[] = []
      let cursor: string | undefined
      do {
        const page = await call(api.GET('/api/v1/projects', { params: { query: { limit: 200, cursor } }, signal }))
        items.push(...page.items)
        cursor = page.next_cursor || undefined
      } while (cursor)
      return items
    },
    staleTime: 60_000,
  })
}

export function useProject(id: string | undefined) {
  return useQuery({
    queryKey: keys.projects.detail(id ?? ''),
    queryFn: ({ signal }) => call(api.GET('/api/v1/projects/{id}', { params: { path: { id: id! } }, signal })),
    enabled: !!id,
  })
}

function useProjectResult() {
  const qc = useQueryClient()
  return (project: Project) => {
    qc.setQueryData(keys.projects.detail(project.id), project)
    void qc.invalidateQueries({ queryKey: keys.projects.list() })
  }
}

export function useCreateProject() {
  const apply = useProjectResult()
  return useMutation({
    mutationFn: (body: ProjectFields & { id: string }) =>
      call(api.POST('/api/v1/projects', { params: { header: CSRF }, body })),
    onSuccess: apply,
  })
}

export function useUpdateProject(id: string) {
  const apply = useProjectResult()
  return useMutation({
    mutationFn: (body: ProjectFields) =>
      call(api.PATCH('/api/v1/projects/{id}', { params: { path: { id }, header: CSRF }, body })),
    onSuccess: apply,
  })
}

export function useArchiveProject(id: string) {
  const apply = useProjectResult()
  return useMutation({
    mutationFn: () => call(api.DELETE('/api/v1/projects/{id}', { params: { path: { id }, header: CSRF } })),
    onSuccess: apply,
    onError: toastError,
  })
}

export function useAddRepository(projectId: string) {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: (body: { full_name: string; installation_id: number; base_branch?: string; is_default?: boolean }) =>
      call(api.POST('/api/v1/projects/{id}/repositories', { params: { path: { id: projectId }, header: CSRF }, body })),
    onSuccess: () => qc.invalidateQueries({ queryKey: keys.projects.all }),
    onError: toastError,
  })
}

export function useRemoveRepository(projectId: string) {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: (repoId: number) =>
      call(
        api.DELETE('/api/v1/projects/{id}/repositories/{repoId}', {
          params: { path: { id: projectId, repoId }, header: CSRF },
        }),
      ),
    onSuccess: () => qc.invalidateQueries({ queryKey: keys.projects.all }),
    onError: toastError,
  })
}

export function useGitHubRepos(enabled: boolean) {
  return useQuery({
    queryKey: keys.githubRepos,
    queryFn: ({ signal }) => call(api.GET('/api/v1/github/repositories', { signal })),
    enabled,
    staleTime: 5 * 60_000,
    retry: (count, e) => !isApiError(e, 'github_disabled') && count < 2,
  })
}

// ---- System ----

export function useStats() {
  return useQuery({
    queryKey: keys.stats,
    queryFn: ({ signal }) => call(api.GET('/api/v1/stats', { signal })),
    refetchInterval: 30_000,
  })
}

export function useHealth() {
  return useQuery({
    queryKey: keys.health,
    queryFn: async ({ signal }) => {
      // 503 carries the same body with the failing check.
      const res = await api.GET('/healthz', { signal })
      return ((res.data ?? res.error) as Health | undefined) ?? { ok: res.response.ok, checks: {} }
    },
    refetchInterval: 60_000,
  })
}
