import { useCallback, useEffect, useMemo, useRef, useState, useSyncExternalStore, type PointerEvent as ReactPointerEvent } from 'react'
import { Link } from 'react-router'
import { Eye, LayoutGrid, Laptop, Maximize, Minus, Plus, Server } from 'lucide-react'
import type { Task } from '@/api/types'
import { useNodes, useTasks } from '@/api/hooks'
import { useTasksSubscription } from '@/realtime/RealtimeProvider'
import { hostInfo, hostOrder } from '@/lib/hosts'
import { storage } from '@/lib/storage'
import { cn } from '@/lib/utils'
import { Button } from '@/components/ui/button'
import { TaskList } from '@/routes/tasks/TaskList'
import { t } from '@/i18n/en'
import { TerminalWindow } from './TerminalWindow'
import {
  fitView,
  hostBlocks,
  HEADER_H,
  layoutWindows,
  MIN_H,
  MIN_W,
  windowHeight,
  zoomAt,
  type View,
  type WindowState,
} from './layout'
import './canvas.css'

const tc = t.canvas
const STORE = 'fleetpocket.canvas.v1'

/** Host accents: the server, then one per computer (cycled). */
const SERVER_ACCENT = '#7aa2f7'
const NODE_ACCENTS = ['#4fd1b0', '#f0a35e', '#e98bd0', '#9ccf6a', '#6fc3f0']

type Filter = 'live' | 'all'
type Saved = { view: View; windows: Record<string, WindowState>; hidden: number[]; filter: Filter }

function load(): Saved | null {
  try {
    const raw = storage.get(STORE)
    return raw ? (JSON.parse(raw) as Saved) : null
  } catch {
    return null
  }
}

const desktopQuery = '(min-width: 1024px) and (pointer: fine)'
function useDesktop() {
  return useSyncExternalStore(
    (cb) => {
      const m = window.matchMedia(desktopQuery)
      m.addEventListener('change', cb)
      return () => m.removeEventListener('change', cb)
    },
    () => window.matchMedia(desktopQuery).matches,
  )
}

export default function CanvasPage() {
  const desktop = useDesktop()
  if (!desktop) {
    return (
      <div className="flex h-full flex-col">
        <p className="shrink-0 border-b px-4 py-3 text-sm text-muted-foreground">{tc.desktopOnly}</p>
        <div className="min-h-0 flex-1">
          <TaskList />
        </div>
      </div>
    )
  }
  return <Canvas />
}

function Canvas() {
  useTasksSubscription()
  const [saved] = useState(load)
  const [filter, setFilter] = useState<Filter>(saved?.filter ?? 'live')
  const active = useTasks({ group: 'active' })
  const prOpen = useTasks({ group: 'pr_open' })
  const done = useTasks({ group: 'done' })
  const failed = useTasks({ group: 'failed' })
  const nodes = useNodes()

  const [hidden, setHidden] = useState<number[]>(saved?.hidden ?? [])
  const tasks = useMemo(() => {
    const pages = filter === 'live' ? [active] : [active, prOpen, done, failed]
    const all = pages.flatMap((q) => q.data?.pages.flatMap((p) => p.items) ?? [])
    // "All recent": live work plus the latest finished tasks of each group.
    const recent = filter === 'all' ? all.filter((tk, i, arr) => arr.findIndex((x) => x.id === tk.id) === i).slice(0, 40) : all
    return recent.filter((tk) => !hidden.includes(tk.id))
  }, [filter, active, prOpen, done, failed, hidden])

  const hosts = useMemo(() => hostOrder(nodes.data, tasks.map((tk) => tk.node)), [nodes.data, tasks])
  const accent = useCallback(
    (id: string) => (id ? NODE_ACCENTS[Math.max(0, hosts.indexOf(id) - 1) % NODE_ACCENTS.length] : SERVER_ACCENT),
    [hosts],
  )

  const [windows, setWindows] = useState<Record<string, WindowState>>(saved?.windows ?? {})
  // Place windows of tasks that appeared since the last layout.
  const placed = useMemo(() => layoutWindows(tasks, hosts, windows), [tasks, hosts, windows])

  const boardRef = useRef<HTMLDivElement>(null)
  const [view, setView] = useState<View>(saved?.view ?? { x: 48, y: 72, zoom: 0.9 })
  const [order, setOrder] = useState<number[]>([])
  const [focused, setFocused] = useState<number | null>(null)

  // Persist (per browser): positions, view, hidden windows, filter.
  useEffect(() => {
    const id = window.setTimeout(() => storage.set(STORE, JSON.stringify({ view, windows: placed, hidden, filter } satisfies Saved)), 300)
    return () => window.clearTimeout(id)
  }, [view, placed, hidden, filter])

  const fit = useCallback(() => {
    const el = boardRef.current
    if (!el) return
    const rects = tasks.map((tk) => placed[tk.id]).filter(Boolean).map((w) => ({ ...w, h: windowHeight(w) }))
    setView(fitView(rects, el.clientWidth, el.clientHeight))
  }, [tasks, placed])

  const tidy = () => setWindows(layoutWindows(tasks, hosts, placed, true))

  // ---- pointer: pan the board, move and resize windows ----
  const drag = useRef<
    | { kind: 'pan'; sx: number; sy: number; v: View }
    | { kind: 'move' | 'resize'; id: number; sx: number; sy: number; w: WindowState }
    | null
  >(null)

  function onBoardPointerDown(e: ReactPointerEvent) {
    if (e.button !== 0 || e.target !== e.currentTarget) return
    drag.current = { kind: 'pan', sx: e.clientX, sy: e.clientY, v: view }
    e.currentTarget.setPointerCapture(e.pointerId)
    setFocused(null)
  }

  const startWindowDrag = (id: number) => (e: ReactPointerEvent, kind: 'move' | 'resize') => {
    if (e.button !== 0) return
    e.stopPropagation()
    drag.current = { kind, id, sx: e.clientX, sy: e.clientY, w: placed[id] }
    boardRef.current?.setPointerCapture(e.pointerId)
  }

  function onPointerMove(e: ReactPointerEvent) {
    const d = drag.current
    if (!d) return
    if (d.kind === 'pan') {
      setView({ ...d.v, x: d.v.x + e.clientX - d.sx, y: d.v.y + e.clientY - d.sy })
      return
    }
    const dx = (e.clientX - d.sx) / view.zoom
    const dy = (e.clientY - d.sy) / view.zoom
    const next: WindowState =
      d.kind === 'move'
        ? { ...d.w, x: Math.round(d.w.x + dx), y: Math.round(d.w.y + dy) }
        : { ...d.w, w: Math.max(MIN_W, Math.round(d.w.w + dx)), h: Math.max(MIN_H, Math.round(d.w.h + dy)) }
    setWindows((ws) => ({ ...ws, ...placed, [d.id]: next }))
  }

  function onPointerUp() {
    drag.current = null
  }

  // Wheel pans; ctrl/cmd + wheel (and trackpad pinch) zooms around the pointer.
  useEffect(() => {
    const el = boardRef.current
    if (!el) return
    const onWheel = (e: WheelEvent) => {
      e.preventDefault()
      const r = el.getBoundingClientRect()
      if (e.ctrlKey || e.metaKey) {
        setView((v) => zoomAt(v, Math.exp(-e.deltaY / 300), e.clientX - r.left, e.clientY - r.top))
      } else {
        setView((v) => ({ ...v, x: v.x - e.deltaX, y: v.y - e.deltaY }))
      }
    }
    el.addEventListener('wheel', onWheel, { passive: false })
    return () => el.removeEventListener('wheel', onWheel)
  }, [])

  const zoomBy = (f: number) => {
    const el = boardRef.current
    if (el) setView((v) => zoomAt(v, f, el.clientWidth / 2, el.clientHeight / 2))
  }

  function focus(id: number) {
    setFocused(id)
    setOrder((o) => (o[o.length - 1] === id ? o : [...o.filter((x) => x !== id), id]))
  }

  const columns = hostBlocks(tasks, hosts).map((b) => ({ ...b, info: hostInfo(b.id, nodes.data), count: tasks.filter((tk) => (tk.node ?? '') === b.id).length }))
  const loading = active.isPending || nodes.isPending

  return (
    <div className="relative h-full overflow-hidden">
      <div
        ref={boardRef}
        className="fp-board absolute inset-0 cursor-default"
        style={{ '--zoom': view.zoom, '--bx': `${view.x}px`, '--by': `${view.y}px` } as React.CSSProperties}
        onPointerDown={onBoardPointerDown}
        onPointerMove={onPointerMove}
        onPointerUp={onPointerUp}
        onPointerCancel={onPointerUp}
      >
        <div
          className="pointer-events-none absolute top-0 left-0 origin-top-left"
          style={{ transform: `translate(${view.x}px, ${view.y}px) scale(${view.zoom})` }}
        >
          {columns.map((c) => (
            <ColumnHeader key={c.id || 'server'} x={c.x} width={c.width} name={c.info.name} online={c.info.online} server={!c.id} count={c.count} accent={accent(c.id)} />
          ))}
          <div className="pointer-events-auto">
            {tasks.map((tk: Task) => {
              const w = placed[tk.id]
              if (!w) return null
              const h = hostInfo(tk.node, nodes.data)
              const z = order.indexOf(tk.id) + 1
              return (
                <TerminalWindow
                  key={tk.id}
                  task={tk}
                  win={w}
                  z={z}
                  focused={focused === tk.id}
                  host={{ name: h.name, online: h.online, accent: accent(tk.node ?? '') }}
                  onDragStart={startWindowDrag(tk.id)}
                  onFocus={() => focus(tk.id)}
                  onToggleMin={() => setWindows((ws) => ({ ...ws, ...placed, [tk.id]: { ...w, min: !w.min } }))}
                  onHide={() => setHidden((hs) => [...hs, tk.id])}
                />
              )
            })}
          </div>
        </div>
        {!loading && tasks.length === 0 && (
          <div className="absolute inset-0 flex items-center justify-center">
            <div className="pointer-events-auto rounded-xl border bg-background/90 px-6 py-5 text-center shadow-sm">
              <p className="text-sm text-muted-foreground">{tc.empty}</p>
              <Button asChild className="mt-3 h-10">
                <Link to="/new">{t.tasks.createFirst}</Link>
              </Button>
            </div>
          </div>
        )}
      </div>

      {/* Toolbar */}
      <div className="absolute top-3 left-3 flex items-center gap-1 rounded-lg border bg-background/90 p-1 shadow-sm backdrop-blur">
        <div role="radiogroup" aria-label={tc.show} className="flex">
          {(['live', 'all'] as const).map((f) => (
            <button
              key={f}
              role="radio"
              aria-checked={filter === f}
              onClick={() => setFilter(f)}
              className={cn('h-8 rounded-md px-3 text-sm font-medium text-muted-foreground', filter === f && 'bg-accent text-foreground')}
            >
              {tc.filters[f]}
            </button>
          ))}
        </div>
        <span className="mx-1 h-5 w-px bg-border" aria-hidden="true" />
        <ToolButton label={tc.zoomOut} onClick={() => zoomBy(1 / 1.2)}>
          <Minus />
        </ToolButton>
        <button className="h-8 w-12 rounded-md text-xs text-muted-foreground tabular-nums hover:bg-accent" onClick={() => setView((v) => ({ ...v, zoom: 1 }))} title="100%">
          {Math.round(view.zoom * 100)}%
        </button>
        <ToolButton label={tc.zoomIn} onClick={() => zoomBy(1.2)}>
          <Plus />
        </ToolButton>
        <ToolButton label={tc.fit} onClick={fit}>
          <Maximize />
        </ToolButton>
        <ToolButton label={tc.tidy} onClick={tidy}>
          <LayoutGrid />
        </ToolButton>
        {hidden.length > 0 && (
          <>
            <span className="mx-1 h-5 w-px bg-border" aria-hidden="true" />
            <Button variant="ghost" size="sm" className="h-8" onClick={() => setHidden([])} title={tc.showHidden}>
              <Eye /> {tc.hidden(hidden.length)}
            </Button>
          </>
        )}
      </div>
    </div>
  )
}

function ToolButton({ label, onClick, children }: { label: string; onClick: () => void; children: React.ReactNode }) {
  return (
    <Button variant="ghost" size="icon" className="size-8 [&_svg]:size-4" onClick={onClick} aria-label={label} title={label}>
      {children}
    </Button>
  )
}

/** The plate above a host's column: which machine these terminals run on. */
function ColumnHeader({ x, width, name, online, server, count, accent }: { x: number; width: number; name: string; online: boolean; server: boolean; count: number; accent: string }) {
  const Icon = server ? Server : Laptop
  return (
    <div className="absolute flex items-center gap-2.5" style={{ left: x, top: 0, width, height: HEADER_H - 16 }}>
      <span className="flex size-8 items-center justify-center rounded-md text-white" style={{ background: accent }}>
        <Icon className="size-4" aria-hidden="true" />
      </span>
      <div className="min-w-0">
        <div className="truncate text-[15px] leading-tight font-semibold">{name}</div>
        <div className="text-xs text-muted-foreground">
          {online ? t.computers.online : t.computers.offline}, {tc.tasks(count)}
        </div>
      </div>
    </div>
  )
}
