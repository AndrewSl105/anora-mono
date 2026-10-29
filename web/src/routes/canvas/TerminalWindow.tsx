import { memo, useEffect, useMemo, useRef, useState, type PointerEvent as ReactPointerEvent } from 'react'
import { Link } from 'react-router'
import { Maximize2, Minus, OctagonPause, X } from 'lucide-react'
import type { Task, TaskStatus, TaskView } from '@/api/types'
import { useAnswer, useSendMessage, useTask, useTaskAction, useTaskEvents } from '@/api/hooks'
import { useTaskSubscription } from '@/realtime/RealtimeProvider'
import { buildTimeline, isStopping, type TimelineItem } from '@/lib/timeline'
import { can, composerMode, STATUS } from '@/lib/status'
import { usd } from '@/lib/format'
import { cn } from '@/lib/utils'
import { withOpenQuestion } from '@/routes/tasks/Timeline'
import { dividerText } from '@/routes/tasks/TimelineRow'
import { t } from '@/i18n/en'
import { TITLE_H, type WindowState } from './layout'

const tc = t.canvas

/** Status light colours (the terminal is dark in both themes). */
const LED: Record<TaskStatus, string> = {
  queued: '#8390a5',
  preparing: '#7aa2f7',
  running: '#7aa2f7',
  waiting_input: '#f5c56b',
  paused: '#8390a5',
  pushing: '#7aa2f7',
  pr_open: '#c4a7ff',
  completed: '#7fd1a4',
  failed: '#f28b82',
  cancelled: '#8390a5',
}
const LIVE: TaskStatus[] = ['running', 'waiting_input', 'preparing', 'pushing']

/** At most this many timeline lines are drawn per terminal (the full page has them all). */
const MAX_LINES = 160

type Props = {
  task: Task
  win: WindowState
  host: { name: string; online: boolean; accent: string }
  focused: boolean
  z: number
  onDragStart: (e: ReactPointerEvent, kind: 'move' | 'resize') => void
  onFocus: () => void
  onToggleMin: () => void
  onHide: () => void
}

export const TerminalWindow = memo(function TerminalWindow({ task, win, host, focused, z, onDragStart, onFocus, onToggleMin, onHide }: Props) {
  const status = STATUS[task.status]
  return (
    <section
      aria-label={task.title}
      data-focused={focused}
      onPointerDownCapture={onFocus}
      className="fp-term absolute flex flex-col overflow-hidden rounded-lg"
      style={
        {
          left: win.x,
          top: win.y,
          width: win.w,
          height: win.min ? TITLE_H : win.h,
          zIndex: z,
          '--host': host.accent,
        } as React.CSSProperties
      }
    >
      <header
        className="fp-term-bar flex shrink-0 cursor-grab items-center gap-2 pr-1 pl-3 select-none active:cursor-grabbing"
        style={{ height: TITLE_H }}
        onPointerDown={(e) => {
          if ((e.target as HTMLElement).closest('button,a')) return
          onDragStart(e, 'move')
        }}
        onDoubleClick={onToggleMin}
      >
        <span
          className="fp-led size-2 shrink-0 rounded-full"
          style={{ color: LED[task.status] }}
          data-live={LIVE.includes(task.status)}
          role="img"
          aria-label={status.label}
          title={status.label}
        />
        <h3 className="min-w-0 flex-1 truncate text-[13px] font-medium" title={task.title}>
          {task.title}
        </h3>
        <span className="shrink-0 text-[11px] text-[var(--term-dim)]">
          {t.agents[task.agent]} · #{task.id}
        </span>
        <div className="flex shrink-0">
          <BarButton label={tc.open} asLink={`/tasks/${task.id}`}>
            <Maximize2 className="size-3.5" />
          </BarButton>
          <BarButton label={win.min ? tc.restore : tc.minimize} onClick={onToggleMin}>
            <Minus className="size-3.5" />
          </BarButton>
          <BarButton label={tc.close} onClick={onHide}>
            <X className="size-3.5" />
          </BarButton>
        </div>
      </header>
      {!win.min && (
        <>
          <TerminalBody taskId={task.id} host={host} />
          <div
            className="absolute right-0 bottom-0 size-4 cursor-nwse-resize"
            onPointerDown={(e) => onDragStart(e, 'resize')}
            aria-hidden="true"
          />
        </>
      )}
    </section>
  )
})

function BarButton({ label, onClick, asLink, children }: { label: string; onClick?: () => void; asLink?: string; children: React.ReactNode }) {
  const cls =
    'flex size-7 items-center justify-center rounded text-[var(--term-dim)] hover:bg-white/8 hover:text-[var(--term-fg)] focus-visible:outline focus-visible:outline-1 focus-visible:outline-[var(--host)]'
  if (asLink) {
    return (
      <Link to={asLink} className={cls} aria-label={label} title={label}>
        {children}
      </Link>
    )
  }
  return (
    <button type="button" className={cls} onClick={onClick} aria-label={label} title={label}>
      {children}
    </button>
  )
}

/** The live part: loads and follows the task's events while the window is open. */
function TerminalBody({ taskId, host }: { taskId: number; host: Props['host'] }) {
  const task = useTask(taskId)
  const events = useTaskEvents(taskId)
  useTaskSubscription(taskId)
  const view = task.data
  const items = useMemo(() => {
    const all = buildTimeline(events.data?.items ?? [], events.data?.local ?? [])
    const withQ = view ? withOpenQuestion(all, view) : all
    return withQ.slice(-MAX_LINES)
  }, [events.data, view])

  const scrollRef = useRef<HTMLDivElement>(null)
  const pinned = useRef(true)
  useEffect(() => {
    const el = scrollRef.current
    if (el && pinned.current) el.scrollTop = el.scrollHeight
  }, [items])

  const stopping = view ? isStopping(events.data?.items ?? [], view.status) : false
  const openQuestion = view?.open_question && !view.open_question.answer ? view.open_question.id : null

  return (
    <>
      <div
        ref={scrollRef}
        onScroll={(e) => {
          const el = e.currentTarget
          pinned.current = el.scrollHeight - el.scrollTop - el.clientHeight < 24
        }}
        // Wheel inside a terminal scrolls it instead of panning the board.
        onWheel={(e) => e.stopPropagation()}
        className="fp-term-body min-h-0 flex-1 overflow-y-auto px-3 py-2"
      >
        {!host.online && <p className="text-[var(--term-ask)]">{t.hosts.waitingFor(host.name)}</p>}
        {items.length === 0 && <p className="text-[var(--term-dim)]">{events.isPending ? t.common.loading : tc.noActivity}</p>}
        {items.map((item) => (
          <Line key={item.key} item={item} taskId={taskId} open={item.kind === 'question' && item.questionId === openQuestion} />
        ))}
      </div>
      {view && <Prompt task={view} stopping={stopping} />}
    </>
  )
}

const Line = memo(function Line({ item, taskId, open }: { item: TimelineItem; taskId: number; open: boolean }) {
  switch (item.kind) {
    case 'claude':
      return <p className="my-1 whitespace-pre-wrap">{item.text}</p>
    case 'user':
      return (
        <p className={cn('my-1 whitespace-pre-wrap text-[var(--host)]', item.pending && 'opacity-60')}>
          <span aria-hidden="true">❯ </span>
          {item.text}
        </p>
      )
    case 'attachments':
      return <p className="text-[var(--term-dim)]">{t.attachments.taskImages(item.attachments.length)}</p>
    case 'tool':
      return (
        <p className="truncate text-[var(--term-dim)]" title={item.summary}>
          <span className={item.ok === false ? 'text-[var(--term-bad)]' : item.ok ? 'text-[var(--term-ok)]' : ''}>
            {item.ok === false ? '✗' : item.ok ? '✓' : '·'}
          </span>{' '}
          <span className="text-[var(--term-fg)]">{item.tool}</span> {item.summary.startsWith(item.tool) ? item.summary.slice(item.tool.length) : item.summary}
        </p>
      )
    case 'question':
      return <Question item={item} taskId={taskId} open={open} />
    case 'divider':
      return <p className={cn('my-1 text-[var(--term-dim)]', item.tone === 'warn' && 'text-[var(--term-ask)]')}>── {dividerText(item.text)}</p>
    case 'status':
      return (
        <p className="my-1 text-[var(--term-dim)]">
          ── {STATUS[item.status].label}
          {item.reason ? `: ${item.reason}` : ''}
        </p>
      )
    case 'commit':
      return (
        <p className="text-[var(--term-ok)]">
          ● {item.sha.slice(0, 7)} {t.task.timeline.committed(item.files)} +{item.insertions} −{item.deletions}
        </p>
      )
    case 'pr': {
      const text =
        item.action === 'opened' ? t.task.timeline.prOpened(item.number) : item.action === 'updated' ? t.task.timeline.prUpdated(item.number) : t.task.timeline.prClosed(item.number, !!item.merged)
      return (
        <p className="text-[#c4a7ff]">
          ⇡ {item.url ? <a href={item.url} target="_blank" rel="noreferrer" className="underline underline-offset-2">{text}</a> : text}
        </p>
      )
    }
    case 'ci':
      return <p className="text-[var(--term-dim)]">{t.task.timeline.ci(item.status, item.name)}</p>
    case 'done':
      return (
        <div className="my-1.5 border-l-2 border-[var(--term-ok)] pl-2 whitespace-pre-wrap">
          {item.noChanges ? t.task.timeline.noChanges : item.summary || t.task.timeline.done}
        </div>
      )
    case 'error':
      return (
        <p className="my-1 text-[var(--term-bad)]">
          ✗ {item.title}: {item.message}
        </p>
      )
  }
})

function Question({ item, taskId, open }: { item: Extract<TimelineItem, { kind: 'question' }>; taskId: number; open: boolean }) {
  const answer = useAnswer(taskId)
  const [text, setText] = useState('')
  const interactive = open && !item.answer && !answer.isPending
  const send = (v: string) => {
    if (!v.trim() || !interactive) return
    answer.mutate({ question_id: item.questionId, text: v.trim() }, { onSuccess: () => setText('') })
  }
  return (
    <div className="my-1.5 rounded border border-[color-mix(in_oklab,var(--term-ask)_45%,transparent)] px-2 py-1.5">
      <p className="text-[var(--term-ask)]">? {item.text}</p>
      {item.answer ? (
        <p className="mt-1 text-[var(--host)]">❯ {item.answer.text}</p>
      ) : (
        interactive && (
          <>
            {item.options.length > 0 && (
              <div className="mt-1.5 flex flex-wrap gap-1.5">
                {item.options.map((o) => (
                  <button
                    key={o}
                    type="button"
                    onClick={() => send(o)}
                    className="rounded border border-[var(--term-rule)] px-2 py-0.5 hover:border-[var(--term-ask)] hover:text-[var(--term-ask)]"
                  >
                    {o}
                  </button>
                ))}
              </div>
            )}
            <form
              className="mt-1.5 flex items-center gap-1"
              onSubmit={(e) => {
                e.preventDefault()
                send(text)
              }}
            >
              <span className="text-[var(--term-ask)]" aria-hidden="true">
                ❯
              </span>
              <input
                value={text}
                onChange={(e) => setText(e.target.value)}
                placeholder={tc.answer}
                aria-label={tc.answer}
                className="fp-term-input min-w-0 flex-1 outline-none"
              />
            </form>
          </>
        )
      )}
    </div>
  )
}

/** One-line composer: queues a message for the next run or starts a follow-up. */
function Prompt({ task, stopping }: { task: TaskView; stopping: boolean }) {
  const mode = composerMode(task.status)
  const send = useSendMessage(task.id)
  const action = useTaskAction(task.id)
  const [text, setText] = useState('')
  if (!mode) return null
  const label = mode === 'queue' ? tc.prompt : tc.followup
  return (
    <form
      className="fp-term-body flex shrink-0 items-center gap-2 border-t border-[var(--term-rule)] px-3 py-1.5"
      onSubmit={(e) => {
        e.preventDefault()
        const v = text.trim()
        if (!v || send.isPending) return
        setText('')
        send.mutate({ text: v, queued: mode === 'queue' }, { onError: () => setText(v) })
      }}
    >
      <span className="text-[var(--host)]" aria-hidden="true">
        ❯
      </span>
      <input
        value={text}
        onChange={(e) => setText(e.target.value)}
        placeholder={task.queued_message ? `${t.task.composer.queued}: ${task.queued_message}` : label}
        aria-label={label}
        className="fp-term-input min-w-0 flex-1 outline-none"
      />
      <span className="shrink-0 text-[11px] text-[var(--term-dim)] tabular-nums">{usd(task.cost_usd_total)}</span>
      {can(task.status, 'interrupt') && !stopping && (
        <button
          type="button"
          onClick={() => action.mutate('interrupt')}
          className="flex size-6 items-center justify-center rounded text-[var(--term-dim)] hover:text-[var(--term-bad)]"
          aria-label={tc.stop}
          title={tc.stop}
        >
          <OctagonPause className="size-3.5" />
        </button>
      )}
    </form>
  )
}
