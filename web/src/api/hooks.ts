import {
  useInfiniteQuery,
  useMutation,
  useQuery,
  useQueryClient,
  type QueryClient,
} from '@tanstack/react-query'
import { toast } from 'sonner'
import { api, call, CSRF, uploadForm, type UploadProgress } from './client'
import { keys, type TaskFilter } from './keys'
import {
  addLocalEvent,
  lastEventId,
  mergeEvents,
  prependEvents,
  removeLocalEvent,
  upsertTask,
  type EventsData,
} from './cache'
import type { Agent, ApiEvent, Health, HostNode, Project, ProjectFields, Run, TaskView } from './types'
import { isApiError } from './errors'
import { GROUPS } from '@/lib/status'
import { t } from '@/i18n/en'
import { haptic } from '@/telegram/haptics'

const EVENTS_PAGE = 200

/**
 * openapi-fetch's response transform (openapi-typescript-helpers 0.1.0
 * `Readable<T>`) drops properties typed exactly `null`, so backend events lose
 * `seq: null` in the response type. The payload is the generated `Event` union.
 */
function asEvents(items: unknown[]): ApiEvent[] {
  return items as ApiEvent[]
}

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
 * The first load opens the timeline at its end: the newest page
 * (`before_id=0`, newest first); older pages load on scroll-up
 * (`useLoadOlderEvents`). A refetch (e.g. after a WebSocket reconnect) catches
 * up forward from the last event id in the cache.
 */
export async function fetchEventsIncremental(qc: QueryClient, id: number, signal?: AbortSignal): Promise<EventsData> {
  const key = keys.tasks.events(id)
  let after = lastEventId(qc.getQueryData<EventsData>(key))
  if (!after) {
    const page = await call(
      api.GET('/api/v1/tasks/{id}/events', { params: { path: { id }, query: { before_id: 0, limit: EVENTS_PAGE } }, signal }),
    )
    return prependEvents(qc.getQueryData<EventsData>(key), asEvents(page.items), page.next_cursor)
  }
  const fetched: ApiEvent[] = []
  for (let i = 0; i < 500; i++) {
    const page = await call(
      api.GET('/api/v1/tasks/{id}/events', {
        params: { path: { id }, query: { after_id: after || undefined, limit: EVENTS_PAGE } },
        signal,
      }),
    )
    fetched.push(...asEvents(page.items))
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

/** Loads the next older timeline page (scroll-up). */
export function useLoadOlderEvents(id: number) {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: async (before: string) =>
      call(api.GET('/api/v1/tasks/{id}/events', { params: { path: { id }, query: { before_id: Number(before), limit: EVENTS_PAGE } } })),
    onSuccess: (page) =>
      qc.setQueryData<EventsData>(keys.tasks.events(id), (prev) => prependEvents(prev, asEvents(page.items), page.next_cursor)),
    onError: toastError,
  })
}

export function useTaskRuns(id: number) {
  return useQuery({
    queryKey: keys.tasks.runs(id),
    queryFn: async ({ signal }) => {
      const items: Run[] = []
      let cursor: string | undefined
      do {
        const page = await call(api.GET('/api/v1/tasks/{id}/runs', { params: { path: { id }, query: { limit: 200, cursor } }, signal }))
        items.push(...page.items)
        cursor = page.next_cursor || undefined
      } while (cursor)
      return items
    },
    staleTime: 30_000,
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

type CreateBody = { project_id: string; prompt: string; repository_id?: number; title?: string; agent?: Agent; node?: string }

/** Multipart body: the JSON fields as form fields plus files[] (API.md, "Attachments"). */
export function toFormData(fields: Record<string, string | number | undefined>, files: File[]): FormData {
  const form = new FormData()
  for (const [k, v] of Object.entries(fields)) if (v !== undefined) form.append(k, String(v))
  for (const f of files) form.append('files[]', f, f.name || 'image')
  return form
}

export function useCreateTask() {
  const qc = useQueryClient()
  const apply = useTaskResult()
  return useMutation({
    mutationFn: ({ files = [], onProgress, ...body }: CreateBody & { files?: File[]; onProgress?: UploadProgress }) =>
      files.length
        ? uploadForm<TaskView>('/api/v1/tasks', toFormData(body, files), onProgress)
        : call(api.POST('/api/v1/tasks', { params: { header: CSRF }, body })),
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

/** `queued`: the task has a pending or active run, so the backend queues the text (composerMode). */
export function useSendMessage(taskId: number) {
  const qc = useQueryClient()
  const apply = useTaskResult()
  return useMutation({
    mutationFn: ({ text, files = [], onProgress }: { text: string; queued: boolean; files?: File[]; onProgress?: UploadProgress }) =>
      files.length
        ? uploadForm<TaskView>(`/api/v1/tasks/${taskId}/message`, toFormData({ text }, files), onProgress)
        : call(api.POST('/api/v1/tasks/{id}/message', { params: { path: { id: taskId }, header: CSRF }, body: { text } })),
    onMutate: ({ text, queued }) => {
      const local = addLocalEvent(qc, taskId, 'message', { text, delivery: queued ? 'queued' : 'new_run' })
      return { localId: local.id }
    },
    onSuccess: (task) => {
      haptic.success()
      apply(task)
    },
    onError: (e, _vars, ctx) => {
      if (ctx) removeLocalEvent(qc, taskId, ctx.localId)
      toastError(e)
      // A 409 means the task moved on (e.g. failed); show its current state.
      void qc.invalidateQueries({ queryKey: keys.tasks.detail(taskId) })
    },
  })
}

type TaskActionKind = 'cancel' | 'interrupt' | 'retry' | 'merge'
const ACTION_PATH = {
  cancel: '/api/v1/tasks/{id}/cancel',
  interrupt: '/api/v1/tasks/{id}/interrupt',
  retry: '/api/v1/tasks/{id}/retry',
  merge: '/api/v1/tasks/{id}/merge',
} as const
const ACTION_DONE = { cancel: t.task.cancelled, interrupt: t.task.interrupted, retry: t.task.retried, merge: t.task.merged }

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

export function useUpdateRepository(projectId: string) {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: ({ repoId, ...body }: { repoId: number; base_branch?: string; is_default?: boolean }) =>
      call(
        api.PATCH('/api/v1/projects/{id}/repositories/{repoId}', {
          params: { path: { id: projectId, repoId }, header: CSRF },
          body,
        }),
      ),
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

// ---- Nodes (computers that run tasks) ----

export function useNodes() {
  return useQuery({
    queryKey: keys.nodes,
    queryFn: async ({ signal }) => (await call(api.GET('/api/v1/nodes', { signal }))).items,
    staleTime: 60_000,
  })
}

/** Replaces one node in the cached list (WebSocket `node` messages, mutations). */
export function upsertNode(qc: QueryClient, node: HostNode) {
  qc.setQueryData<HostNode[]>(keys.nodes, (prev) => {
    if (!prev) return prev
    const i = prev.findIndex((n) => n.id === node.id)
    if (i < 0) return [...prev, node]
    const next = prev.slice()
    next[i] = node
    return next
  })
}

export function useCreateNode() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: (body: { id: string; name?: string }) => call(api.POST('/api/v1/nodes', { params: { header: CSRF }, body })),
    onSuccess: (res) => upsertNode(qc, res.node),
  })
}

export function useUpdateNode(id: string) {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: (body: { name?: string; max_concurrent?: number }) =>
      call(api.PATCH('/api/v1/nodes/{id}', { params: { path: { id }, header: CSRF }, body })),
    onSuccess: (node) => upsertNode(qc, node),
    onError: toastError,
  })
}

export function useRotateNodeToken(id: string) {
  return useMutation({
    mutationFn: () => call(api.POST('/api/v1/nodes/{id}/token', { params: { path: { id }, header: CSRF } })),
    onError: toastError,
  })
}

export function useDeleteNode(id: string) {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: () => call(api.DELETE('/api/v1/nodes/{id}', { params: { path: { id }, header: CSRF } })),
    onSuccess: () => {
      qc.setQueryData<HostNode[]>(keys.nodes, (prev) => prev?.filter((n) => n.id !== id))
      void qc.invalidateQueries({ queryKey: keys.projects.all })
    },
    onError: toastError,
  })
}

// ---- System ----

/** The agents tasks may use (those with credentials on the server) and the default. */
export function useAgents() {
  return useQuery({
    queryKey: keys.me,
    queryFn: ({ signal }) => call(api.GET('/api/v1/me', { signal })),
    staleTime: 5 * 60_000,
    select: (s) => ({ agents: s.agents, defaultAgent: s.default_agent }),
  })
}

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

export type AttachmentImage = { state: 'loading' } | { state: 'ready'; blob: Blob } | { state: 'removed' } | { state: 'error' }

/**
 * An attachment (or its thumbnail) as a Blob, shown with BlobImage. Fetched
 * through the API client rather than <img src> because Telegram Web sends a
 * bearer token, which an image request cannot carry. `removed` after a 410.
 */
export function useAttachmentImage(taskId: number, attachmentId: number, thumb: boolean): AttachmentImage {
  const q = useQuery({
    queryKey: keys.tasks.attachment(taskId, attachmentId, thumb),
    queryFn: () =>
      call(
        api.GET('/api/v1/tasks/{id}/attachments/{attachmentId}', {
          params: { path: { id: taskId, attachmentId }, query: thumb ? { thumb: '1' } : {} },
          parseAs: 'blob',
        }),
      ) as Promise<Blob>,
    staleTime: Infinity,
    gcTime: thumb ? 10 * 60_000 : 60_000,
    retry: (n, e) => !isApiError(e, 'gone') && !isApiError(e, 'not_found') && n < 2,
  })
  if (q.error) return isApiError(q.error, 'gone') ? { state: 'removed' } : { state: 'error' }
  return q.data ? { state: 'ready', blob: q.data } : { state: 'loading' }
}
