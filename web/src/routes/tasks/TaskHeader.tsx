import { useNavigate } from 'react-router'
import { toast } from 'sonner'
import { ArrowLeft, Ban, Copy, ExternalLink, GitPullRequest, MoreVertical, OctagonPause, RotateCcw } from 'lucide-react'
import type { TaskView } from '@/api/types'
import { useTaskAction } from '@/api/hooks'
import { Button } from '@/components/ui/button'
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from '@/components/ui/dropdown-menu'
import { StatusBadge } from '@/components/StatusBadge'
import { useConfirm } from '@/components/confirm'
import { can } from '@/lib/status'
import { usd } from '@/lib/format'
import { useRuntime } from '@/telegram/runtime'
import { t } from '@/i18n/en'

export function githubUrl(task: TaskView): string | null {
  if (task.pr_url) return task.pr_url
  const repo = task.repository?.full_name
  if (!repo) return null
  return task.branch ? `https://github.com/${repo}/tree/${encodeURIComponent(task.branch)}` : `https://github.com/${repo}`
}

export function TaskHeader({ task }: { task: TaskView }) {
  const runtime = useRuntime()
  const navigate = useNavigate()
  const confirm = useConfirm()
  const action = useTaskAction(task.id)
  const gh = githubUrl(task)

  async function cancel() {
    if (await confirm(t.task.confirmCancel, t.task.actions.cancel)) action.mutate('cancel')
  }

  async function copyBranch() {
    if (!task.branch) return
    try {
      await navigator.clipboard.writeText(task.branch)
      toast.success(t.common.copied)
    } catch {
      toast.error(t.common.somethingWrong)
    }
  }

  return (
    <header className="shrink-0 px-4 pt-3 pb-3">
      <div className="flex items-start gap-2">
        {runtime.mode === 'browser' && (
          <Button
            variant="ghost"
            size="icon"
            className="-ml-2 size-11 shrink-0 lg:hidden"
            onClick={() => navigate(-1)}
            aria-label={t.common.back}
          >
            <ArrowLeft />
          </Button>
        )}
        <div className="min-w-0 flex-1 pt-1.5">
          <h1 className="text-lg leading-snug font-semibold break-words">{task.title}</h1>
        </div>
        <DropdownMenu>
          <DropdownMenuTrigger asChild>
            <Button variant="ghost" size="icon" className="-mr-2 size-11 shrink-0" aria-label="Task actions">
              <MoreVertical />
            </Button>
          </DropdownMenuTrigger>
          <DropdownMenuContent align="end" className="min-w-48">
            {can(task.status, 'interrupt') && (
              <DropdownMenuItem className="h-10" onSelect={() => action.mutate('interrupt')}>
                <OctagonPause /> {t.task.actions.interrupt}
              </DropdownMenuItem>
            )}
            {can(task.status, 'retry') && (
              <DropdownMenuItem className="h-10" onSelect={() => action.mutate('retry')}>
                <RotateCcw /> {t.task.actions.retry}
              </DropdownMenuItem>
            )}
            {task.branch && (
              <DropdownMenuItem className="h-10" onSelect={copyBranch}>
                <Copy /> {t.task.actions.copyBranch}
              </DropdownMenuItem>
            )}
            {gh && (
              <DropdownMenuItem className="h-10" asChild>
                <a href={gh} target="_blank" rel="noreferrer">
                  <ExternalLink /> {t.task.actions.openGitHub}
                </a>
              </DropdownMenuItem>
            )}
            {can(task.status, 'cancel') && (
              <>
                <DropdownMenuSeparator />
                <DropdownMenuItem className="h-10" variant="destructive" onSelect={cancel}>
                  <Ban /> {t.task.actions.cancel}
                </DropdownMenuItem>
              </>
            )}
          </DropdownMenuContent>
        </DropdownMenu>
      </div>

      <div className="mt-2 flex flex-wrap items-center gap-x-3 gap-y-1.5 text-xs text-muted-foreground">
        <StatusBadge status={task.status} />
        <span className="truncate">
          {task.project_id}
          {task.repository && <span className="font-mono"> / {task.repository.full_name.split('/').pop()}</span>}
        </span>
        <span className="tabular-nums">{usd(task.cost_usd_total)}</span>
        {task.turns > 0 && <span className="tabular-nums">{t.task.turns(task.turns)}</span>}
        {task.pr_url && task.pr_number && (
          <a
            href={task.pr_url}
            target="_blank"
            rel="noreferrer"
            className="inline-flex h-7 items-center gap-1 rounded-full border border-violet-500/40 px-2.5 font-medium text-violet-700 dark:text-violet-300"
          >
            <GitPullRequest className="size-3.5" aria-hidden="true" />
            {t.task.actions.openPr(task.pr_number)}
          </a>
        )}
      </div>
      {task.branch && <p className="mt-1.5 truncate font-mono text-xs text-muted-foreground">{task.branch}</p>}
      {task.status_reason && (task.status === 'failed' || task.status === 'cancelled' || task.status === 'paused') && (
        <p className="mt-2 rounded-md bg-red-500/10 px-2.5 py-1.5 text-xs text-red-700 dark:text-red-300">{task.status_reason}</p>
      )}
    </header>
  )
}
