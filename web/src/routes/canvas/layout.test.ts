import { describe, expect, it } from 'vitest'
import type { Task } from '@/api/types'
import { fitView, GAP, HEADER_H, hostBlocks, layoutWindows, ROWS, WIN_H, WIN_W, zoomAt } from './layout'
import { hostOrder } from '@/lib/hosts'

const task = (id: number, status: Task['status'], node: string | null = null) => ({ id, status, node }) as Task

describe('layoutWindows', () => {
  const tasks = [task(1, 'running'), task(2, 'waiting_input'), task(3, 'running', 'mac'), task(4, 'completed')]
  const hosts = hostOrder([], tasks.map((t) => t.node))

  it('puts each host in its own block, the tasks that need the owner first', () => {
    const w = layoutWindows(tasks, hosts, {})
    expect(hosts).toEqual(['', 'mac'])
    expect(w[2]).toMatchObject({ x: 0, y: HEADER_H })
    expect(w[1]).toMatchObject({ x: 0, y: HEADER_H + WIN_H + GAP })
    expect(w[4].y).toBeGreaterThan(w[1].y)
    const mac = hostBlocks(tasks, hosts)[1]
    expect(w[3]).toMatchObject({ x: mac.x, y: HEADER_H })
    expect(mac.x).toBeGreaterThan(WIN_W)
  })

  it('keeps moved windows and stacks new ones below the column', () => {
    const first = layoutWindows(tasks, hosts, {})
    const moved = { ...first, 1: { ...first[1], x: 2000, y: 900 } }
    const next = layoutWindows([...tasks, task(5, 'queued')], hosts, moved)
    expect(next[1]).toMatchObject({ x: 2000, y: 900 })
    // The new window overlaps none of the kept ones.
    for (const id of [2, 4]) {
      const k = next[id]
      const overlap = next[5].x < k.x + k.w && k.x < next[5].x + next[5].w && next[5].y < k.y + k.h && k.y < next[5].y + next[5].h
      expect(overlap).toBe(false)
    }
    // Tidy puts the moved window back.
    expect(layoutWindows(tasks, hosts, moved, true)[1].x).toBe(0)
  })

  it('wraps a busy host into sub-columns', () => {
    const many = Array.from({ length: ROWS + 1 }, (_, i) => task(i + 1, 'running'))
    const [b] = hostBlocks(many, [''])
    expect(b.span).toBe(2)
    const w = layoutWindows(many, [''], {}, true)
    expect(new Set(Object.values(w).map((x) => x.x)).size).toBe(2)
  })

  it('minimised windows take only their title bar', () => {
    const w = layoutWindows([task(1, 'running'), task(2, 'running')], [''], { 2: { x: 0, y: HEADER_H, w: WIN_W, h: WIN_H, min: true } })
    expect(w[1].y).toBe(HEADER_H + 40 + GAP)
  })
})

describe('view', () => {
  it('fits all windows and zooms around the pointer', () => {
    const v = fitView([{ x: 0, y: HEADER_H, w: 1000, h: 2000 }], 1000, 800)
    expect(v.zoom).toBeLessThan(1)
    const z = zoomAt({ x: 0, y: 0, zoom: 1 }, 2, 100, 100)
    // Clamped at ZOOM_MAX; the point under the pointer stays put.
    expect(z).toEqual({ x: -60, y: -60, zoom: 1.6 })
  })
})
