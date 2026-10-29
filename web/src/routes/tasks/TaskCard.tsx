import { Link } from 'react-router'
import type { Task } from '@/api/types'
import { StatusBadge } from '@/components/StatusBadge'
import { HostChip } from '@/components/HostChip'
import { relativeTime, usd } from '@/lib/format'
import { cn } from '@/lib/utils'
import { t } from '@/i18n/en'

/** One-line activity: the backend's latest tool_use or status change; CI for open PRs. */
function activityLine(task: Task): string | null {
  if (task.status === 'pr_open' && task.ci_status) return `CI ${task.ci_status}`
  return task.last_activity ?? task.status_reason ?? task.branch ?? null
}

export function TaskCard({ task, projectName, selected }: { task: Task; projectName?: string; selected?: boolean }) {
  const waiting = task.status === 'waiting_input'
  const line = activityLine(task)
  return (
    <Link
      to={`/tasks/${task.id}`}
      aria-current={selected ? 'page' : undefined}
      className={cn(
        'block rounded-xl border bg-card p-3.5 transition-colors hover:bg-accent/60 focus-visible:ring-2 focus-visible:ring-ring focus-visible:outline-none',
        waiting && 'border-amber-500/50 bg-amber-500/5',
        selected && 'border-primary/50 bg-accent',
      )}
    >
      <div className="flex items-start justify-between gap-3">
        <h2 className="line-clamp-2 min-w-0 font-medium break-words">{task.title}</h2>
        <StatusBadge status={task.status} />
      </div>
      {line && <p className="mt-1 truncate font-mono text-xs text-muted-foreground">{line}</p>}
      <div className="mt-2 flex items-center gap-2 text-xs text-muted-foreground">
        <span className="truncate">{projectName ?? task.project_id}</span>
        {task.node && (
          <>
            <span aria-hidden="true">·</span>
            <HostChip node={task.node} className="shrink" />
          </>
        )}
        <span aria-hidden="true">·</span>
        <span className="shrink-0">{relativeTime(task.last_activity_at ?? task.updated_at)}</span>
        <span className="ml-auto shrink-0 tabular-nums">{usd(task.cost_usd_total)}</span>
      </div>
      {waiting && <span className="sr-only">{t.tasks.needsAnswer}</span>}
    </Link>
  )
}
