import { memo, useState } from 'react'
import {
  ChevronRight,
  CircleCheck,
  CircleX,
  ExternalLink,
  GitCommitHorizontal,
  GitMerge,
  GitPullRequest,
  GitPullRequestClosed,
  OctagonAlert,
  Sparkles,
} from 'lucide-react'
import type { TimelineItem } from '@/lib/timeline'
import { STATUS } from '@/lib/status'
import { cn } from '@/lib/utils'
import { Markdown } from '@/components/Markdown'
import { t } from '@/i18n/en'

const tl = t.task.timeline

function dividerText(text: string): string {
  if (text.startsWith('run_started:')) return tl.runStarted(text.slice('run_started:'.length))
  if (text.startsWith('run_finished:')) {
    const code = text.slice('run_finished:'.length)
    return tl.runFinished(code === '' ? null : Number(code))
  }
  if (text === 'interrupt') return tl.interrupt
  if (text === 'paused') return tl.paused
  return text
}

function noteText(note: NonNullable<Extract<TimelineItem, { kind: 'user' }>['note']>): string {
  if (note === 'queued') return tl.queued
  if (note === 'injected') return tl.injected
  return tl.dropped(note.dropped)
}

function Divider({ children, warn }: { children: React.ReactNode; warn?: boolean }) {
  return (
    <div className={cn('flex items-center gap-3 py-1 text-[11px] text-muted-foreground', warn && 'text-amber-700 dark:text-amber-300')}>
      <span className="h-px flex-1 bg-border" />
      <span className="max-w-[75%] text-center">{children}</span>
      <span className="h-px flex-1 bg-border" />
    </div>
  )
}

function ToolRow({ item }: { item: Extract<TimelineItem, { kind: 'tool' }> }) {
  const [open, setOpen] = useState(false)
  const [name, ...rest] = item.summary.split(' ')
  const target = item.summary.startsWith(item.tool) ? rest.join(' ') : item.summary
  const label = item.summary.startsWith(item.tool) ? name : item.tool
  const failed = item.ok === false
  return (
    <div>
      <button
        onClick={() => setOpen((o) => !o)}
        aria-expanded={open}
        className={cn(
          'flex min-h-9 w-full items-center gap-2 rounded-md px-2 text-left text-xs text-muted-foreground hover:bg-muted',
          failed && 'text-red-700 dark:text-red-300',
        )}
      >
        <ChevronRight className={cn('size-3.5 shrink-0 transition-transform', open && 'rotate-90')} aria-hidden="true" />
        <span className="shrink-0 font-medium text-foreground/80">{label}</span>
        {target && <span className="truncate font-mono">· {target}</span>}
        {failed && <CircleX className="ml-auto size-3.5 shrink-0" aria-label="failed" />}
      </button>
      {open && (
        <div className="ml-7 rounded-md bg-muted px-2.5 py-1.5 font-mono text-xs break-all text-muted-foreground">
          {item.summary}
          {item.error && <div className="mt-1 text-red-700 dark:text-red-300">{item.error}</div>}
        </div>
      )}
    </div>
  )
}

function TimelineRowImpl({ item }: { item: TimelineItem }) {
  switch (item.kind) {
    case 'claude':
      return (
        <div className="max-w-[92%] rounded-2xl rounded-tl-md bg-surface px-3.5 py-2.5 shadow-xs ring-1 ring-border">
          <Markdown>{item.text}</Markdown>
        </div>
      )
    case 'user':
      return (
        <div className="flex justify-end">
          <div
            className={cn(
              'max-w-[85%] rounded-2xl rounded-tr-md bg-primary px-3.5 py-2.5 text-[15px] whitespace-pre-wrap text-primary-foreground',
              item.pending && 'opacity-70',
            )}
          >
            {item.text}
            {item.pending ? (
              <div className="mt-1 text-[11px] opacity-80">{tl.sending}</div>
            ) : (
              item.note && <div className="mt-1 text-[11px] opacity-80">{noteText(item.note)}</div>
            )}
          </div>
        </div>
      )
    case 'tool':
      return <ToolRow item={item} />
    case 'divider':
      return <Divider warn={item.tone === 'warn'}>{dividerText(item.text)}</Divider>
    case 'status': {
      const label = STATUS[item.status]?.label ?? item.status
      return (
        <Divider>
          {tl.status(label)}
          {item.reason ? ` · ${item.reason}` : ''}
        </Divider>
      )
    }
    case 'commit':
      return (
        <div className="flex items-center gap-2.5 rounded-xl border bg-card px-3 py-2.5 text-sm">
          <GitCommitHorizontal className="size-4 shrink-0 text-muted-foreground" aria-hidden="true" />
          <span>{tl.committed(item.files)}</span>
          <span className="font-mono text-xs text-emerald-700 dark:text-emerald-300">+{item.insertions}</span>
          <span className="font-mono text-xs text-red-700 dark:text-red-300">−{item.deletions}</span>
          <span className="ml-auto font-mono text-xs text-muted-foreground">{item.sha.slice(0, 7)}</span>
        </div>
      )
    case 'pr': {
      const Icon = item.action === 'closed' ? (item.merged ? GitMerge : GitPullRequestClosed) : GitPullRequest
      const text =
        item.action === 'opened'
          ? tl.prOpened(item.number)
          : item.action === 'updated'
            ? tl.prUpdated(item.number)
            : tl.prClosed(item.number, !!item.merged)
      const body = (
        <>
          <Icon className="size-4 shrink-0 text-violet-600 dark:text-violet-300" aria-hidden="true" />
          <span className="font-medium">{text}</span>
          {item.url && <ExternalLink className="ml-auto size-4 text-muted-foreground" aria-hidden="true" />}
        </>
      )
      const cls = 'flex items-center gap-2.5 rounded-xl border border-violet-500/30 bg-violet-500/5 px-3 py-2.5 text-sm'
      return item.url ? (
        <a href={item.url} target="_blank" rel="noreferrer" className={cn(cls, 'hover:bg-violet-500/10')}>
          {body}
        </a>
      ) : (
        <div className={cls}>{body}</div>
      )
    }
    case 'ci': {
      const ok = item.status === 'success'
      const bad = item.status === 'failure'
      const Icon = ok ? CircleCheck : bad ? CircleX : ChevronRight
      return (
        <a
          href={item.url}
          target="_blank"
          rel="noreferrer"
          className={cn(
            'flex min-h-9 items-center gap-2 px-2 text-xs text-muted-foreground',
            ok && 'text-emerald-700 dark:text-emerald-300',
            bad && 'text-red-700 dark:text-red-300',
          )}
        >
          <Icon className="size-3.5" aria-hidden="true" />
          {tl.ci(item.status, item.name)}
        </a>
      )
    }
    case 'done':
      return (
        <div className="rounded-xl border border-emerald-500/30 bg-emerald-500/5 px-3.5 py-3">
          <div className="mb-1.5 flex items-center gap-2 text-sm font-medium text-emerald-700 dark:text-emerald-300">
            <Sparkles className="size-4" aria-hidden="true" />
            {item.noChanges ? tl.noChanges : tl.done}
          </div>
          {item.summary && <Markdown className="text-sm">{item.summary}</Markdown>}
        </div>
      )
    case 'error':
      return (
        <div role="alert" className="flex gap-2.5 rounded-xl border border-red-500/30 bg-red-500/5 px-3 py-2.5 text-sm text-red-700 dark:text-red-300">
          <OctagonAlert className="mt-0.5 size-4 shrink-0" aria-hidden="true" />
          <div className="min-w-0">
            <div className="font-mono text-xs font-medium">{item.title === 'diff_rejected' ? tl.diffRejected : item.title}</div>
            <div className="break-words">{item.message}</div>
          </div>
        </div>
      )
    case 'question':
      return null
  }
}

export const TimelineRow = memo(TimelineRowImpl)
