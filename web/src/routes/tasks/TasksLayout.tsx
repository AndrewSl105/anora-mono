import { Outlet, useParams } from 'react-router'
import { cn } from '@/lib/utils'
import { TaskList } from './TaskList'

/** Phone: list or detail. Desktop (≥ 1024 px): list next to the open task. */
export function TasksLayout() {
  const { id } = useParams()
  return (
    <div className="flex h-full">
      <section className={cn('h-full w-full min-w-0 lg:w-96 lg:shrink-0 lg:border-r', id ? 'hidden lg:block' : 'block')}>
        <TaskList selectedId={id ? Number(id) : undefined} />
      </section>
      <div className={cn('h-full min-w-0 flex-1', id ? 'block' : 'hidden lg:block')}>
        <Outlet />
      </div>
    </div>
  )
}
