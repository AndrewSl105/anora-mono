import { Laptop, Server } from 'lucide-react'
import type { HostNode } from '@/api/types'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { SERVER } from '@/lib/hosts'
import { t } from '@/i18n/en'

const DEFAULT = '__default' // Radix Select items cannot have an empty value

type Props = {
  id: string
  /** '' is the default choice (see `defaultLabel`), else 'server' or a node id. */
  value: string
  onChange: (v: string) => void
  nodes: HostNode[]
  /** Label of the '' choice; omit to offer only the server and the nodes ('' then means the server). */
  defaultLabel?: string
}

/** Where tasks run: the server or one of the owner's computers (offline ones marked). */
export function HostSelect({ id, value, onChange, nodes, defaultLabel }: Props) {
  const current = value || (defaultLabel ? DEFAULT : SERVER)
  return (
    <Select
      value={current}
      onValueChange={(v) => onChange(v === DEFAULT || (!defaultLabel && v === SERVER) ? '' : v)}
    >
      <SelectTrigger id={id} className="h-11 w-full">
        <SelectValue />
      </SelectTrigger>
      <SelectContent>
        {defaultLabel && (
          <SelectItem value={DEFAULT} className="h-10">
            {defaultLabel}
          </SelectItem>
        )}
        <SelectItem value={SERVER} className="h-10">
          <Server className="size-4 text-muted-foreground" aria-hidden="true" />
          {t.hosts.server}
        </SelectItem>
        {nodes.map((n) => (
          <SelectItem key={n.id} value={n.id} className="h-10">
            <Laptop className="size-4 text-muted-foreground" aria-hidden="true" />
            {n.name}
            {!n.online && <span className="text-muted-foreground"> · {t.hosts.offline}</span>}
          </SelectItem>
        ))}
      </SelectContent>
    </Select>
  )
}
