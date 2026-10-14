import type { TaskGroup } from '@/lib/status'

export type TaskFilter = { group: TaskGroup; project?: string }

export const keys = {
  tasks: {
    all: ['tasks'] as const,
    lists: () => ['tasks', 'list'] as const,
    list: (filter: TaskFilter) => ['tasks', 'list', filter] as const,
    detail: (id: number) => ['tasks', 'detail', id] as const,
    events: (id: number) => ['tasks', 'events', id] as const,
    diff: (id: number) => ['tasks', 'diff', id] as const,
  },
  projects: {
    all: ['projects'] as const,
    list: () => ['projects', 'list'] as const,
    detail: (id: string) => ['projects', 'detail', id] as const,
  },
  githubRepos: ['github', 'repositories'] as const,
  stats: ['stats'] as const,
  health: ['health'] as const,
}
