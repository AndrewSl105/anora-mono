import type { TaskStatus } from '@/api/types'
import { STATUS, TONE_CLASSES } from '@/lib/status'
import { cn } from '@/lib/utils'

export function StatusBadge({ status, className }: { status: TaskStatus; className?: string }) {
  const meta = STATUS[status]
  const Icon = meta.icon
  return (
    <span
      className={cn(
        'inline-flex shrink-0 items-center gap-1 rounded-full border px-2 py-0.5 text-xs font-medium whitespace-nowrap',
        TONE_CLASSES[meta.tone],
        className,
      )}
    >
      <Icon className={cn('size-3.5', meta.spin && 'animate-spin [animation-duration:2.5s]')} aria-hidden="true" />
      {meta.label}
    </span>
  )
}
