import type { Task, TaskStatus } from '@/api/types'

/** Window geometry in board coordinates (zoom 1). */
export type Rect = { x: number; y: number; w: number; h: number }
export type WindowState = Rect & { min?: boolean }
export type View = { x: number; y: number; zoom: number }

export const WIN_W = 440
export const WIN_H = 320
export const TITLE_H = 40
export const GAP = 24
export const COLUMN_GAP = 72
/** Room above the windows for the column header. */
export const HEADER_H = 56
export const MIN_W = 300
export const MIN_H = 160
export const ZOOM_MIN = 0.3
export const ZOOM_MAX = 1.6

/** Column order inside a host: what needs the owner first, then live work, then the rest. */
const RANK: Record<TaskStatus, number> = {
  waiting_input: 0,
  paused: 1,
  running: 2,
  preparing: 3,
  pushing: 4,
  queued: 5,
  pr_open: 6,
  failed: 7,
  cancelled: 8,
  completed: 9,
}

export function sortForColumn(tasks: Task[]): Task[] {
  return [...tasks].sort((a, b) => RANK[a.status] - RANK[b.status] || b.id - a.id)
}

/** Windows per sub-column before a host's block wraps into another one. */
export const ROWS = 3

/** A host's block on the board: its sub-columns side by side. */
export type Block = { id: string; x: number; span: number; width: number }

/** Lays the hosts out left to right; a host with many tasks gets several sub-columns. */
export function hostBlocks(tasks: Task[], hosts: string[]): Block[] {
  let x = 0
  return hosts.map((id) => {
    const n = tasks.filter((t) => (t.node ?? '') === id).length
    const span = Math.max(1, Math.ceil(n / ROWS))
    const width = span * WIN_W + (span - 1) * GAP
    const b = { id, x, span, width }
    x += width + COLUMN_GAP
    return b
  })
}

export function windowHeight(w: WindowState): number {
  return w.min ? TITLE_H : w.h
}

/**
 * Places every task: kept windows stay where they are; a new one goes to the
 * lowest point of its host's block (the sub-column with the most room).
 * `tidy` re-stacks everything, the tasks that need the owner first.
 */
export function layoutWindows(
  tasks: Task[],
  hosts: string[],
  prev: Record<string, WindowState>,
  tidy = false,
): Record<string, WindowState> {
  const out: Record<string, WindowState> = {}
  const blocks = new Map(hostBlocks(tasks, hosts).map((b) => [b.id, b]))
  // Bottom of each sub-column, per host.
  const bottoms = new Map<string, number[]>()
  for (const b of blocks.values()) bottoms.set(b.id, Array(b.span).fill(HEADER_H))
  const subX = (b: Block, i: number) => b.x + i * (WIN_W + GAP)

  if (!tidy) {
    for (const t of tasks) {
      const w = prev[t.id]
      if (!w) continue
      out[t.id] = w
      const b = blocks.get(t.node ?? '')
      if (!b) continue
      const col = Math.round((w.x - b.x) / (WIN_W + GAP))
      // Only windows still inside their block push its sub-column down.
      if (col >= 0 && col < b.span && Math.abs(w.x - subX(b, col)) < WIN_W / 2) {
        const bs = bottoms.get(b.id)!
        bs[col] = Math.max(bs[col], w.y + windowHeight(w) + GAP)
      }
    }
  }
  for (const b of blocks.values()) {
    const bs = bottoms.get(b.id)!
    for (const t of sortForColumn(tasks.filter((x) => (x.node ?? '') === b.id))) {
      if (out[t.id]) continue
      // Tidy fills row by row; new windows take the sub-column with the most room.
      const col = bs.indexOf(Math.min(...bs))
      const old = prev[t.id]
      const w: WindowState = { x: subX(b, col), y: bs[col], w: WIN_W, h: old?.h ?? WIN_H, min: old?.min }
      out[t.id] = w
      bs[col] += windowHeight(w) + GAP
    }
  }
  return out
}

/** The view that shows all rects inside a viewport of the given size. */
export function fitView(rects: Rect[], width: number, height: number, pad = 48, top = 72): View {
  if (!rects.length) return { x: pad, y: pad, zoom: 1 }
  const minX = Math.min(...rects.map((r) => r.x))
  const minY = Math.min(...rects.map((r) => r.y)) - HEADER_H
  const maxX = Math.max(...rects.map((r) => r.x + r.w))
  const maxY = Math.max(...rects.map((r) => r.y + r.h))
  const zoom = clampZoom(Math.min((width - 2 * pad) / (maxX - minX), (height - top - pad) / (maxY - minY), 1))
  return { zoom, x: pad - minX * zoom + (width - 2 * pad - (maxX - minX) * zoom) / 2, y: top - minY * zoom }
}

export function clampZoom(z: number): number {
  return Math.min(ZOOM_MAX, Math.max(ZOOM_MIN, z))
}

/** Zooms by `factor` keeping the board point under (px, py) in place. */
export function zoomAt(v: View, factor: number, px: number, py: number): View {
  const zoom = clampZoom(v.zoom * factor)
  const k = zoom / v.zoom
  return { zoom, x: px - (px - v.x) * k, y: py - (py - v.y) * k }
}
