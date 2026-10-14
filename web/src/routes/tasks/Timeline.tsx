import { useEffect, useLayoutEffect, useMemo, useRef, useState } from 'react'
import { useVirtualizer } from '@tanstack/react-virtual'
import { ArrowDown, Loader2 } from 'lucide-react'
import type { TaskView } from '@/api/types'
import type { TimelineItem, TimelineSummary } from '@/lib/timeline'
import { can } from '@/lib/status'
import { Skeleton } from '@/components/ui/skeleton'
import { t } from '@/i18n/en'
import { TimelineRow } from './TimelineRow'
import { QuestionCard } from './QuestionCard'

const NEAR_BOTTOM_PX = 80
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

export function Timeline({ task, summary, loading }: { task: TaskView; summary: TimelineSummary; loading: boolean }) {
  const items = useMemo(() => withOpenQuestion(summary.items, task), [summary.items, task])
  const scrollRef = useRef<HTMLDivElement>(null)
  const atBottom = useRef(true)
  const [showPill, setShowPill] = useState(false)
  const prevCount = useRef(0)

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

  // Follow new events while at the bottom; otherwise show "New activity".
  useLayoutEffect(() => {
    const grew = items.length > prevCount.current
    const first = prevCount.current === 0
    prevCount.current = items.length
    if (!grew) return
    if (first || atBottom.current) scrollToEnd()
    else setShowPill(true)
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [items.length])

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
  }

  const working = WORKING.includes(task.status)
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
        )}
        {working && (
          <div className="flex items-center gap-2 pb-3 text-xs text-muted-foreground" role="status">
            <Loader2 className="size-3.5 animate-spin text-primary" aria-hidden="true" />
            <span className="shrink-0 font-medium">{t.task.working}</span>
            {summary.latestTool && <span className="truncate font-mono">· {summary.latestTool}</span>}
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
