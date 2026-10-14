import { lazy, Suspense, useMemo, useState } from 'react'
import { useParams } from 'react-router'
import { useTask, useTaskEvents } from '@/api/hooks'
import { isApiError } from '@/api/errors'
import { useTaskSubscription } from '@/realtime/RealtimeProvider'
import { buildTimeline } from '@/lib/timeline'
import { cn } from '@/lib/utils'
import { haptic } from '@/telegram/haptics'
import { useDisableVerticalSwipes } from '@/telegram/controls'
import { PageSpinner } from '@/components/PageSpinner'
import { NotFound } from '@/routes/NotFound'
import { t } from '@/i18n/en'
import { TaskHeader } from './TaskHeader'
import { Timeline } from './Timeline'
import { Composer } from './Composer'
import { InfoTab } from './InfoTab'

const DiffTab = lazy(() => import('./DiffTab'))

type Tab = 'activity' | 'diff' | 'info'
const TABS: Tab[] = ['activity', 'diff', 'info']

export function TaskDetailPage() {
  const id = Number(useParams().id)
  if (!Number.isInteger(id) || id <= 0) return <NotFound title={t.task.notFound} />
  return <TaskDetail key={id} id={id} />
}

function TaskDetail({ id }: { id: number }) {
  const [tab, setTab] = useState<Tab>('activity')
  const task = useTask(id)
  const events = useTaskEvents(id)
  useTaskSubscription(id)
  useDisableVerticalSwipes()

  const summary = useMemo(() => buildTimeline(events.data?.items ?? [], events.data?.local ?? []), [events.data])

  if (task.isPending) return <PageSpinner />
  if (task.isError) {
    if (isApiError(task.error, 'not_found')) return <NotFound title={t.task.notFound} />
    return <NotFound title={t.common.somethingWrong} />
  }
  const view = task.data

  return (
    <div className="flex h-full flex-col">
      <TaskHeader task={view} turns={summary.turns} />
      <div role="tablist" aria-label="Task sections" className="flex shrink-0 gap-1 border-b px-2">
        {TABS.map((name) => (
          <button
            key={name}
            role="tab"
            aria-selected={tab === name}
            onClick={() => {
              haptic.select()
              setTab(name)
            }}
            className={cn(
              'relative h-11 px-3 text-sm font-medium text-muted-foreground transition-colors',
              tab === name && 'text-foreground after:absolute after:inset-x-2 after:bottom-0 after:h-0.5 after:rounded-full after:bg-primary',
            )}
          >
            {t.task.tabs[name]}
          </button>
        ))}
      </div>
      <div className="flex min-h-0 flex-1 flex-col">
        {tab === 'activity' && (
          <>
            <Timeline task={view} summary={summary} loading={events.isPending} />
            <Composer task={view} />
          </>
        )}
        {tab === 'diff' && (
          <Suspense fallback={<PageSpinner />}>
            <DiffTab taskId={id} />
          </Suspense>
        )}
        {tab === 'info' && <InfoTab task={view} runs={summary.runs} />}
      </div>
    </div>
  )
}

export default TaskDetailPage
