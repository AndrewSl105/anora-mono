import { useEffect, useLayoutEffect, useMemo, useRef, useState } from 'react'
import { useVirtualizer } from '@tanstack/react-virtual'
import { ArrowDown, Loader2 } from 'lucide-react'
import type { TaskView } from '@/api/types'
import { useLoadOlderEvents } from '@/api/hooks'
import type { TimelineItem } from '@/lib/timeline'
import { can } from '@/lib/status'
import { Skeleton } from '@/components/ui/skeleton'
import { t } from '@/i18n/en'
import { TimelineRow } from './TimelineRow'
import { QuestionCard } from './QuestionCard'

const NEAR_BOTTOM_PX = 80
const NEAR_TOP_PX = 300
const WORKING: TaskView['status'][] = ['preparing', 'running', 'pushing']

/** Adds the task's open question when its event is not loaded yet. */
export function withOpenQuestion(items: TimelineItem[], task: TaskView): TimelineItem[] {
  const q = task.open_question
  if (!q || q.answer || items.some((i) => i.kind === 'question' && i.questionId === q.id)) return items
  return [
    ...items,
    { kind: 'question', key: `open-${q.id}`, questionId: q.id, text: q.text, options: q.options ?? [], at: q.created_at },
  ]
}

type Props = {
  task: TaskView
  items: TimelineItem[]
  /** before_id cursor of the next older page; "" when the start is loaded. */
  older: string
  loading: boolean
  /** An interrupt was sent; the run is ending. */
  stopping: boolean
}

export function Timeline({ task, items: loaded, older, loading, stopping }: Props) {
  const items = useMemo(() => withOpenQuestion(loaded, task), [loaded, task])
  const scrollRef = useRef<HTMLDivElement>(null)
  const atBottom = useRef(true)
  const [showPill, setShowPill] = useState(false)
  const prev = useRef<{ first?: string; last?: string; height: number }>({ height: 0 })
  const loadOlder = useLoadOlderEvents(task.id)

  const virtualizer = useVirtualizer({
    count: items.length,
    getScrollElement: () => scrollRef.current,
    estimateSize: (i) => (items[i].kind === 'tool' || items[i].kind === 'divider' || items[i].kind === 'status' ? 36 : 88),
    overscan: 10,
    getItemKey: (i) => items[i].key,
  })

  function scrollToEnd() {
    if (items.length === 0) return
    virtualizer.scrollToIndex(items.length - 1, { align: 'end' })
    setShowPill(false)
  }

  // New rows at the end: follow them while at the bottom, otherwise show
  // "New activity". Older rows in front (scroll-up paging): keep the rows on
  // screen in place by shifting the scroll position by the added height.
  useLayoutEffect(() => {
    const first = items[0]?.key
    const last = items[items.length - 1]?.key
    const before = prev.current
    const height = virtualizer.getTotalSize()
    prev.current = { first, last, height }
    if (!last) return
    if (before.first !== undefined && first !== before.first && last === before.last) {
      const el = scrollRef.current
      if (el) el.scrollTop += height - before.height
      return
    }
    if (last === before.last) return
    if (before.last === undefined || atBottom.current) scrollToEnd()
    else setShowPill(true)
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [items])

  function maybeLoadOlder() {
    const el = scrollRef.current
    if (el && older && !loadOlder.isPending && el.scrollTop < NEAR_TOP_PX) loadOlder.mutate(older)
  }

  useEffect(() => {
    // Re-pin after late measurement of the last rows.
    if (atBottom.current && items.length) {
      const id = requestAnimationFrame(() => {
        const el = scrollRef.current
        if (el) el.scrollTop = el.scrollHeight
      })
      return () => cancelAnimationFrame(id)
    }
  }, [items, virtualizer])

  function onScroll() {
    const el = scrollRef.current
    if (!el) return
    atBottom.current = el.scrollHeight - el.scrollTop - el.clientHeight < NEAR_BOTTOM_PX
    if (atBottom.current) setShowPill(false)
    maybeLoadOlder()
  }

  const working = WORKING.includes(task.status) || stopping
  const answerable = can(task.status, 'answer')

  return (
    <div className="relative min-h-0 flex-1">
      <div ref={scrollRef} onScroll={onScroll} className="h-full overflow-y-auto overscroll-contain px-4" aria-live="polite">
        {loading ? (
          <div className="space-y-3 py-4">
            <Skeleton className="h-16 w-4/5 rounded-2xl" />
            <Skeleton className="h-8 w-3/5" />
            <Skeleton className="h-24 w-4/5 rounded-2xl" />
          </div>
        ) : items.length === 0 ? (
          <p className="py-10 text-center text-sm text-muted-foreground">{t.task.timeline.empty}</p>
        ) : (
          <>
            {older ? (
              <button
                type="button"
                onClick={() => loadOlder.mutate(older)}
                disabled={loadOlder.isPending}
                className="mx-auto mt-3 flex h-9 items-center gap-1.5 rounded-full px-3 text-xs text-muted-foreground hover:bg-accent"
              >
                {loadOlder.isPending && <Loader2 className="size-3.5 animate-spin" aria-hidden="true" />}
                {loadOlder.isPending ? t.task.timeline.earlier : t.task.timeline.loadEarlier}
              </button>
            ) : (
              <p className="pt-3 text-center text-xs text-muted-foreground">{t.task.timeline.start}</p>
            )}
            <div className="relative w-full py-3" style={{ height: virtualizer.getTotalSize() + 24 }}>
              {virtualizer.getVirtualItems().map((row) => {
                const item = items[row.index]
                return (
                  <div
                    key={row.key}
                    data-index={row.index}
                    ref={virtualizer.measureElement}
                    className="absolute top-0 left-0 w-full py-1"
                    style={{ transform: `translateY(${row.start + 12}px)` }}
                  >
                    {item.kind === 'question' ? (
                      <QuestionCard
                        taskId={task.id}
                        item={item}
                        open={answerable && !item.answer && task.open_question?.id === item.questionId}
                      />
                    ) : (
                      <TimelineRow item={item} />
                    )}
                  </div>
                )
              })}
            </div>
          </>
        )}
        {working && (
          <div className="flex items-center gap-2 pb-3 text-xs text-muted-foreground" role="status">
            <Loader2 className="size-3.5 animate-spin text-primary" aria-hidden="true" />
            <span className="shrink-0 font-medium">{stopping ? t.task.stopping : t.task.working}</span>
            {!stopping && task.last_activity && <span className="truncate font-mono">· {task.last_activity}</span>}
          </div>
        )}
      </div>
      {showPill && (
        <button
          onClick={scrollToEnd}
          className="absolute bottom-3 left-1/2 inline-flex h-9 -translate-x-1/2 items-center gap-1.5 rounded-full bg-primary px-4 text-xs font-medium text-primary-foreground shadow-lg animate-in fade-in-0 slide-in-from-bottom-2"
        >
          <ArrowDown className="size-3.5" aria-hidden="true" />
          {t.task.newActivity}
        </button>
      )}
    </div>
  )
}
