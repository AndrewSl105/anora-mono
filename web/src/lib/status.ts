import {
  CircleCheck,
  CircleDashed,
  CircleHelp,
  CirclePause,
  CircleSlash,
  CircleX,
  GitPullRequest,
  Loader,
  Upload,
  Wrench,
  type LucideIcon,
} from 'lucide-react'
import type { TaskStatus } from '@/api/types'
import { t } from '@/i18n/en'

export type Tone = 'accent' | 'amber' | 'violet' | 'green' | 'red' | 'grey'

export const TONE_CLASSES: Record<Tone, string> = {
  accent: 'bg-primary/12 text-primary border-primary/25',
  amber: 'bg-amber-500/15 text-amber-700 border-amber-500/30 dark:text-amber-300',
  violet: 'bg-violet-500/15 text-violet-700 border-violet-500/30 dark:text-violet-300',
  green: 'bg-emerald-500/15 text-emerald-700 border-emerald-500/30 dark:text-emerald-300',
  red: 'bg-red-500/15 text-red-700 border-red-500/30 dark:text-red-300',
  grey: 'bg-muted text-muted-foreground border-border',
}

export const STATUS: Record<TaskStatus, { label: string; tone: Tone; icon: LucideIcon; spin?: boolean }> = {
  queued: { label: t.status.queued, tone: 'grey', icon: CircleDashed },
  preparing: { label: t.status.preparing, tone: 'accent', icon: Wrench },
  running: { label: t.status.running, tone: 'accent', icon: Loader, spin: true },
  waiting_input: { label: t.status.waiting_input, tone: 'amber', icon: CircleHelp },
  paused: { label: t.status.paused, tone: 'grey', icon: CirclePause },
  pushing: { label: t.status.pushing, tone: 'accent', icon: Upload },
  pr_open: { label: t.status.pr_open, tone: 'violet', icon: GitPullRequest },
  completed: { label: t.status.completed, tone: 'green', icon: CircleCheck },
  failed: { label: t.status.failed, tone: 'red', icon: CircleX },
  cancelled: { label: t.status.cancelled, tone: 'grey', icon: CircleSlash },
}

export const ALL_STATUSES = Object.keys(STATUS) as TaskStatus[]

export type TaskGroup = 'active' | 'pr_open' | 'done' | 'failed'

export const GROUPS: Record<TaskGroup, TaskStatus[]> = {
  active: ['queued', 'preparing', 'running', 'waiting_input', 'paused', 'pushing'],
  pr_open: ['pr_open'],
  done: ['completed'],
  failed: ['failed', 'cancelled'],
}

export const ACTIVE_STATUSES = GROUPS.active

// Actions per status, from the status table in docs/backend/API.md.
export type TaskAction = 'cancel' | 'message' | 'interrupt' | 'answer' | 'retry'

const ACTIONS: Record<TaskStatus, TaskAction[]> = {
  queued: ['cancel'],
  preparing: ['cancel'],
  running: ['message', 'interrupt', 'cancel'],
  waiting_input: ['answer', 'message', 'interrupt', 'cancel'],
  paused: ['answer', 'message', 'cancel'],
  pushing: ['cancel'],
  pr_open: ['message', 'cancel'],
  completed: ['message'],
  failed: ['retry'],
  cancelled: ['retry'],
}

export function can(status: TaskStatus, action: TaskAction): boolean {
  return ACTIONS[status].includes(action)
}

/** How a composer message is delivered, or null when the composer is hidden (409 states). */
export function composerMode(status: TaskStatus): 'inject' | 'followup' | null {
  if (!can(status, 'message')) return null
  return status === 'running' || status === 'waiting_input' ? 'inject' : 'followup'
}
