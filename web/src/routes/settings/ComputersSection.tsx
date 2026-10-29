import { useState } from 'react'
import { KeyRound, Laptop, Plus, Trash2 } from 'lucide-react'
import { toast } from 'sonner'
import type { HostNode } from '@/api/types'
import { errorMessage, useCreateNode, useDeleteNode, useNodes, useRotateNodeToken, useUpdateNode } from '@/api/hooks'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Skeleton } from '@/components/ui/skeleton'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { CopyButton } from '@/components/CopyButton'
import { useConfirm } from '@/components/confirm'
import { relativeTime } from '@/lib/format'
import { useSearchParams } from 'react-router'
import { AddComputerDialog } from './AddComputerDialog'
import { useTasksSubscription } from '@/realtime/RealtimeProvider'
import { cn } from '@/lib/utils'
import { t } from '@/i18n/en'

const c = t.computers
const AGENT_IMAGE = 'ghcr.io/andrewsl105/fleetpocket-agent:latest'

/** Settings → Computers: register machines that run tasks (fleetpocket-node). */
export function ComputersSection() {
  const nodes = useNodes()
  // Node online/offline changes arrive with the task list's WebSocket scope.
  useTasksSubscription()
  const [params, setParams] = useSearchParams()
  const pairCode = params.get('pair') ?? undefined
  // Setup on the computer opens /settings?pair=<code>: start with that code.
  const [wizard, setWizard] = useState(!!pairCode)
  const [adding, setAdding] = useState(false)
  const [token, setToken] = useState<{ id: string; token: string } | null>(null)

  return (
    <section>
      <div className="mb-3 flex items-center justify-between gap-3">
        <h2 className="text-sm font-semibold tracking-wide text-muted-foreground uppercase">{c.title}</h2>
        <Button variant="outline" size="sm" className="h-9" onClick={() => setWizard(true)}>
          <Plus /> {c.add}
        </Button>
      </div>
      <p className="mb-3 text-sm text-muted-foreground">{c.hint}</p>
      {nodes.isPending ? (
        <Skeleton className="h-20 rounded-xl" />
      ) : (
        <ul className="space-y-2">
          {(nodes.data ?? []).map((n) => (
            <NodeRow key={n.id} node={n} onToken={(tok) => setToken({ id: n.id, token: tok })} />
          ))}
        </ul>
      )}
      <AddComputerDialog
        open={wizard}
        initialCode={pairCode}
        onOpenChange={(v) => {
          setWizard(v)
          if (!v && pairCode) setParams({}, { replace: true })
        }}
        onManual={() => {
          setWizard(false)
          setAdding(true)
        }}
      />
      <AddDialog open={adding} onOpenChange={setAdding} onCreated={(id, tok) => setToken({ id, token: tok })} />
      <TokenDialog value={token} onClose={() => setToken(null)} />
    </section>
  )
}

function StatusDot({ node }: { node: HostNode }) {
  const label = node.online ? c.online : node.last_seen_at ? c.offline : c.neverConnected
  return (
    <span className="inline-flex items-center gap-1.5 text-xs text-muted-foreground">
      <span className={cn('size-2 rounded-full', node.online ? 'bg-emerald-500' : 'bg-muted-foreground/40')} aria-hidden="true" />
      {label}
      {!node.online && node.last_seen_at && <span>· {c.lastSeen(relativeTime(node.last_seen_at))}</span>}
    </span>
  )
}

function NodeRow({ node, onToken }: { node: HostNode; onToken: (token: string) => void }) {
  const confirm = useConfirm()
  const update = useUpdateNode(node.id)
  const rotate = useRotateNodeToken(node.id)
  const remove = useDeleteNode(node.id)

  return (
    <li className="rounded-xl border bg-card p-3.5">
      <div className="flex items-start gap-3">
        <Laptop className="mt-0.5 size-5 shrink-0 text-muted-foreground" aria-hidden="true" />
        <div className="min-w-0 flex-1">
          <div className="flex flex-wrap items-baseline gap-x-2">
            <span className="font-medium">{node.name}</span>
            <span className="font-mono text-xs text-muted-foreground">{node.id}</span>
          </div>
          <StatusDot node={node} />
          <div className="mt-1.5 flex flex-wrap items-center gap-1.5 text-xs">
            {node.agents.length ? (
              node.agents.map((a) => (
                <span key={a} className="rounded-full border px-2 py-0.5">
                  {t.agents[a]}
                </span>
              ))
            ) : (
              <span className="text-muted-foreground">{c.noAgents}</span>
            )}
            {node.platform && <span className="font-mono text-muted-foreground">{node.platform}</span>}
            <span className="text-muted-foreground tabular-nums">{c.active(node.active_tasks, node.max_concurrent)}</span>
          </div>
        </div>
      </div>
      <div className="mt-3 flex flex-wrap items-center gap-2">
        <Label htmlFor={`cap-${node.id}`} className="text-xs text-muted-foreground">
          {c.capacity}
        </Label>
        <Select value={String(node.max_concurrent)} onValueChange={(v) => update.mutate({ max_concurrent: Number(v) })}>
          <SelectTrigger id={`cap-${node.id}`} className="h-9 w-20">
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            {[1, 2, 3, 4, 6, 8].map((n) => (
              <SelectItem key={n} value={String(n)}>
                {n}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
        <div className="ml-auto flex gap-1.5">
          <Button
            variant="ghost"
            size="sm"
            className="h-9"
            disabled={rotate.isPending}
            onClick={async () => {
              if (!(await confirm(c.confirmRotate, c.rotate))) return
              const res = await rotate.mutateAsync()
              onToken(res.token)
            }}
          >
            <KeyRound /> {c.rotate}
          </Button>
          <Button
            variant="ghost"
            size="sm"
            className="h-9 text-destructive hover:text-destructive"
            disabled={remove.isPending}
            onClick={async () => {
              if (!(await confirm(c.confirmRemove(node.name), c.remove))) return
              remove.mutate(undefined, { onSuccess: () => toast.success(c.removed) })
            }}
          >
            <Trash2 /> {c.remove}
          </Button>
        </div>
      </div>
    </li>
  )
}

function AddDialog({
  open,
  onOpenChange,
  onCreated,
}: {
  open: boolean
  onOpenChange: (v: boolean) => void
  onCreated: (id: string, token: string) => void
}) {
  const create = useCreateNode()
  const [id, setId] = useState('')
  const [name, setName] = useState('')
  const valid = /^[a-z0-9][a-z0-9-]{0,31}$/.test(id) && id !== 'server'

  async function submit() {
    try {
      const res = await create.mutateAsync({ id, name: name.trim() || undefined })
      onOpenChange(false)
      setId('')
      setName('')
      onCreated(res.node.id, res.token)
    } catch (e) {
      toast.error(errorMessage(e))
    }
  }

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>{c.add}</DialogTitle>
          <DialogDescription>{c.hint}</DialogDescription>
        </DialogHeader>
        <form
          className="space-y-4"
          onSubmit={(e) => {
            e.preventDefault()
            if (valid) void submit()
          }}
        >
          <div className="space-y-2">
            <Label htmlFor="node-name">{c.name}</Label>
            <Input
              id="node-name"
              className="h-11"
              value={name}
              placeholder={c.namePlaceholder}
              onChange={(e) => {
                setName(e.target.value)
                if (!id || id === slug(name)) setId(slug(e.target.value))
              }}
            />
          </div>
          <div className="space-y-2">
            <Label htmlFor="node-id">{c.id}</Label>
            <Input id="node-id" className="h-11 font-mono" value={id} onChange={(e) => setId(e.target.value.toLowerCase())} />
            <p className="text-xs text-muted-foreground">{c.idHint}</p>
          </div>
          <Button type="submit" className="h-11 w-full" disabled={!valid || create.isPending}>
            {c.create}
          </Button>
        </form>
      </DialogContent>
    </Dialog>
  )
}

export function slug(s: string): string {
  return s
    .toLowerCase()
    .normalize('NFKD')
    .replace(/[^a-z0-9]+/g, '-')
    .replace(/^-+|-+$/g, '')
    .slice(0, 32)
}

function Command({ text }: { text: string }) {
  return (
    <div className="flex items-start gap-1 rounded-lg bg-muted py-1.5 pr-1 pl-3">
      <code className="min-w-0 flex-1 py-1 font-mono text-xs break-all whitespace-pre-wrap">{text}</code>
      <CopyButton text={text} />
    </div>
  )
}

function TokenDialog({ value, onClose }: { value: { id: string; token: string } | null; onClose: () => void }) {
  const server = window.location.origin
  return (
    <Dialog open={!!value} onOpenChange={(o) => !o && onClose()}>
      <DialogContent className="max-w-lg">
        <DialogHeader>
          <DialogTitle>{c.tokenTitle}</DialogTitle>
          <DialogDescription>{c.tokenOnce}</DialogDescription>
        </DialogHeader>
        {value && (
          <div className="space-y-3 text-sm">
            <Command text={value.token} />
            <p>{c.step1}</p>
            <Command text={`docker pull ${AGENT_IMAGE}`} />
            <p>{c.step2}</p>
            <Command
              text={`security add-generic-password -U -s fleetpocket-node -a FLEETPOCKET_NODE_TOKEN -w\nsecurity add-generic-password -U -s fleetpocket-node -a CLAUDE_CODE_OAUTH_TOKEN -w`}
            />
            <p>{c.step3}</p>
            <Command text={`fleetpocket-node run --server ${server}`} />
            <Button className="h-11 w-full" onClick={onClose}>
              {c.done}
            </Button>
          </div>
        )}
      </DialogContent>
    </Dialog>
  )
}
