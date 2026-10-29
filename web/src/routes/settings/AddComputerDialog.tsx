import { useEffect, useState } from 'react'
import { Check, Laptop, Loader2, TriangleAlert } from 'lucide-react'
import { toast } from 'sonner'
import { errorMessage, useApprovePairing, useNodes, usePendingPairing } from '@/api/hooks'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { CopyButton } from '@/components/CopyButton'
import { cn } from '@/lib/utils'
import { t } from '@/i18n/en'

const w = t.computers.wizard

/** fleetpocket-node setup answers here while it waits (server/cmd/fleetpocket-node/setup.go). */
export const LOCAL_PAIR_URL = 'http://127.0.0.1:47641/pairing'

type Local = { code: string; hostname: string; platform: string }

/**
 * Asks a setup running on this computer for its code. Only pages of this
 * origin can read it (the node checks Origin), so a hit means "this computer".
 */
export async function findLocalSetup(signal?: AbortSignal): Promise<Local | null> {
  try {
    const res = await fetch(LOCAL_PAIR_URL, { signal, cache: 'no-store' })
    if (!res.ok) return null
    const body = (await res.json()) as Local
    return body.code ? body : null
  } catch {
    return null
  }
}

export function formatCode(raw: string): string {
  const c = raw.toUpperCase().replace(/[^0-9A-Z]/g, '').slice(0, 6)
  return c.length > 3 ? `${c.slice(0, 3)}-${c.slice(3)}` : c
}

type Props = {
  open: boolean
  onOpenChange: (v: boolean) => void
  /** A code from the link setup opened (?pair=). */
  initialCode?: string
  onManual: () => void
}

/** Add a computer: one command on the computer, one click (or a code) here. */
export function AddComputerDialog({ open, onOpenChange, initialCode, onManual }: Props) {
  const [code, setCode] = useState(initialCode ? formatCode(initialCode) : '')
  const [local, setLocal] = useState<Local | null>(null)
  const [paired, setPaired] = useState<{ id: string; name: string } | null>(null)
  const command = `curl -fsSL ${window.location.origin}/api/v1/node/install | sh`

  // While the dialog is open, look for a setup waiting on this computer.
  useEffect(() => {
    if (!open || paired) return
    const ctl = new AbortController()
    let timer = 0
    const tick = async () => {
      const found = await findLocalSetup(ctl.signal)
      if (ctl.signal.aborted) return
      if (found) {
        setLocal(found)
        setCode((c) => c || found.code)
      }
      timer = window.setTimeout(() => void tick(), 2000)
    }
    void tick()
    return () => {
      ctl.abort()
      window.clearTimeout(timer)
    }
  }, [open, paired])

  function close(v: boolean) {
    onOpenChange(v)
    if (!v) {
      setPaired(null)
      setCode('')
      setLocal(null)
    }
  }

  return (
    <Dialog open={open} onOpenChange={close}>
      <DialogContent className="max-w-lg">
        <DialogHeader>
          <DialogTitle>{w.title}</DialogTitle>
          {!paired && <DialogDescription>{w.intro}</DialogDescription>}
        </DialogHeader>
        {paired ? (
          <Paired id={paired.id} name={paired.name} onDone={() => close(false)} />
        ) : (
          <div className="space-y-5">
            <div className="flex items-start gap-1 rounded-lg bg-muted py-1.5 pr-1 pl-3">
              <code className="min-w-0 flex-1 py-1 font-mono text-xs break-words">{command}</code>
              <CopyButton text={command} />
            </div>
            <Confirm code={code} setCode={setCode} local={local} onPaired={setPaired} />
            <button type="button" onClick={onManual} className="text-xs text-muted-foreground underline underline-offset-2">
              {w.manual}
            </button>
          </div>
        )}
      </DialogContent>
    </Dialog>
  )
}

function Confirm({
  code,
  setCode,
  local,
  onPaired,
}: {
  code: string
  setCode: (v: string) => void
  local: Local | null
  onPaired: (n: { id: string; name: string }) => void
}) {
  const pending = usePendingPairing(code)
  const approve = useApprovePairing()
  // null: not edited yet, the computer's own name shows.
  const [edited, setName] = useState<string | null>(null)
  const p = pending.data
  const name = edited ?? p?.hostname ?? ''
  const here = !!local && !!p && local.code === p.code
  const complete = code.replace('-', '').length === 6

  async function connect() {
    if (!p) return
    try {
      const node = await approve.mutateAsync({ code: p.code, name: name.trim() || undefined })
      onPaired({ id: node.id, name: node.name })
    } catch (e) {
      toast.error(errorMessage(e))
    }
  }

  return (
    <div className="space-y-3">
      {!p && (
        <p className="flex items-center gap-2 text-sm text-muted-foreground" aria-live="polite">
          <Loader2 className="size-4 animate-spin" aria-hidden="true" /> {w.waiting}
        </p>
      )}
      {!p && <p className="text-xs text-muted-foreground">{w.waitingHint}</p>}
      <div className="space-y-1.5">
        <Label htmlFor="pair-code">{w.codeLabel}</Label>
        <Input
          id="pair-code"
          value={code}
          onChange={(e) => setCode(formatCode(e.target.value))}
          placeholder={w.codePlaceholder}
          autoComplete="off"
          className="h-11 font-mono tracking-widest uppercase"
        />
        {complete && pending.isError && <p className="text-xs text-destructive">{w.notFound}</p>}
      </div>

      {p && (
        <div className={cn('space-y-3 rounded-xl border p-3.5', here ? 'border-emerald-500/40' : 'border-amber-500/40')}>
          <div className="flex items-start gap-3">
            <Laptop className="mt-0.5 size-5 shrink-0 text-muted-foreground" aria-hidden="true" />
            <div className="min-w-0 text-sm">
              <div className="font-medium">{p.hostname || p.platform}</div>
              <div className="text-xs text-muted-foreground">
                {p.platform}
                {p.agents.length > 0 && `, ${p.agents.map((a) => t.agents[a as keyof typeof t.agents] ?? a).join(', ')}`}
              </div>
              {here ? (
                <div className="mt-1 text-xs font-medium text-emerald-700 dark:text-emerald-300">{w.thisComputer}</div>
              ) : (
                <div className="mt-1 flex items-start gap-1.5 text-xs text-amber-800 dark:text-amber-300">
                  <TriangleAlert className="mt-px size-3.5 shrink-0" aria-hidden="true" />
                  <span>
                    {w.otherWarning} {w.from(p.ip)}
                  </span>
                </div>
              )}
            </div>
          </div>
          <div className="space-y-1.5">
            <Label htmlFor="pair-name">{w.nameLabel}</Label>
            <Input id="pair-name" className="h-11" value={name} onChange={(e) => setName(e.target.value)} />
          </div>
          <Button className="h-11 w-full" onClick={() => void connect()} disabled={approve.isPending}>
            {w.connect}
          </Button>
        </div>
      )}
    </div>
  )
}

/** After pairing: setup finishes on the computer, then it comes online (WebSocket `node` message). */
function Paired({ id, name, onDone }: { id: string; name: string; onDone: () => void }) {
  const [online, setOnline] = useState(false)
  // Pushed over the WebSocket; polled too, in case a message was missed.
  const nodes = useNodes(!online)
  const node = nodes.data?.find((n) => n.id === id)
  if (node?.online && !online) setOnline(true)
  return (
    <div className="space-y-4" aria-live="polite">
      <p className="flex items-center gap-2 font-medium">
        <Check className="size-5 text-emerald-600" aria-hidden="true" /> {w.connected(name)}
      </p>
      {online ? (
        <p className="flex items-center gap-2 text-sm">
          <span className="size-2 rounded-full bg-emerald-500" aria-hidden="true" /> {w.online(name)}
          {node && node.agents.length > 0 && <span className="text-muted-foreground">({node.agents.map((a) => t.agents[a]).join(', ')})</span>}
        </p>
      ) : (
        <p className="flex items-start gap-2 text-sm text-muted-foreground">
          <Loader2 className="mt-0.5 size-4 shrink-0 animate-spin" aria-hidden="true" /> {w.finishing}
        </p>
      )}
      <Button className="h-11 w-full" variant={online ? 'default' : 'outline'} onClick={onDone}>
        {w.done}
      </Button>
    </div>
  )
}
