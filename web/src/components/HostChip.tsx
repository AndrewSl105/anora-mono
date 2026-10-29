import { Laptop, Server } from 'lucide-react'
import { useNodes } from '@/api/hooks'
import { hostInfo } from '@/lib/hosts'
import { cn } from '@/lib/utils'
import { t } from '@/i18n/en'

/**
 * Where a task runs: a small chip with a server or laptop icon. Renders
 * nothing for server tasks unless `always` (single-host setups see no change).
 */
export function HostChip({ node, always, className }: { node: string | null | undefined; always?: boolean; className?: string }) {
  const nodes = useNodes()
  if (!node && !always) return null
  const h = hostInfo(node, nodes.data)
  const Icon = h.id ? Laptop : Server
  return (
    <span
      className={cn('inline-flex min-w-0 items-center gap-1 text-xs text-muted-foreground', className)}
      title={h.online ? h.name : `${h.name} · ${t.hosts.offline}`}
    >
      <Icon className="size-3.5 shrink-0" aria-hidden="true" />
      <span className="truncate">{h.name}</span>
      {h.id && (
        <span
          className={cn('size-1.5 shrink-0 rounded-full', h.online ? 'bg-emerald-500' : 'bg-muted-foreground/40')}
          aria-label={h.online ? t.computers.online : t.computers.offline}
        />
      )}
    </span>
  )
}
