import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { QueryClient } from '@tanstack/react-query'
import { RealtimeClient, backoffDelay, wsUrl } from './socket'
import { catchUp, handleMessage } from './RealtimeProvider'
import { keys } from '@/api/keys'
import type { EventsData } from '@/api/cache'
import type { ApiEvent } from '@/api/types'

class FakeSocket {
  static all: FakeSocket[] = []
  readyState = 0
  sent: unknown[] = []
  onopen: ((ev: Event) => void) | null = null
  onclose: ((ev: CloseEvent) => void) | null = null
  onmessage: ((ev: MessageEvent) => void) | null = null
  onerror: ((ev: Event) => void) | null = null
  url: string
  constructor(url: string) {
    this.url = url
    FakeSocket.all.push(this)
  }
  send(data: string) {
    this.sent.push(JSON.parse(data))
  }
  close() {
    this.readyState = 3
  }
  open() {
    this.readyState = 1
    this.onopen?.(new Event('open'))
  }
  drop() {
    this.readyState = 3
    this.onclose?.(new CloseEvent('close'))
  }
}

function makeClient(onOpen = vi.fn(), onMessage = vi.fn()) {
  return {
    client: new RealtimeClient({
      url: () => 'ws://test/api/v1/ws',
      onOpen,
      onMessage,
      createSocket: (url) => new FakeSocket(url),
      random: () => 1,
      backgroundCloseMs: 60_000,
    }),
    onOpen,
    onMessage,
  }
}

beforeEach(() => {
  FakeSocket.all = []
  vi.useFakeTimers()
})
afterEach(() => vi.useRealTimers())

describe('RealtimeClient', () => {
  it('sends subscriptions once open and reference-counts them', () => {
    const { client } = makeClient()
    client.start()
    const s = FakeSocket.all[0]
    const offA = client.subscribe({ task_id: 5 })
    const offB = client.subscribe({ task_id: 5 })
    s.open()
    expect(s.sent).toEqual([{ op: 'subscribe', task_id: 5 }])
    offA()
    expect(s.sent).toHaveLength(1)
    offB()
    expect(s.sent[1]).toEqual({ op: 'unsubscribe', task_id: 5 })
  })

  it('reconnects with backoff, re-subscribes and triggers catch-up', () => {
    const { client, onOpen } = makeClient()
    client.start()
    client.subscribe({ scope: 'tasks' })
    client.subscribe({ task_id: 9 })
    FakeSocket.all[0].open()
    expect(onOpen).toHaveBeenLastCalledWith([9])

    FakeSocket.all[0].drop()
    expect(client.getStatus()).toBe('closed')
    vi.advanceTimersByTime(999)
    expect(FakeSocket.all).toHaveLength(1)
    vi.advanceTimersByTime(1)
    expect(FakeSocket.all).toHaveLength(2)

    const s2 = FakeSocket.all[1]
    s2.open()
    expect(client.getStatus()).toBe('open')
    expect(s2.sent).toEqual([
      { op: 'subscribe', scope: 'tasks' },
      { op: 'subscribe', task_id: 9 },
    ])
    expect(onOpen).toHaveBeenCalledTimes(2)
  })

  it('closes after 60 s in the background and reconnects on return', () => {
    const { client, onOpen } = makeClient()
    client.start()
    FakeSocket.all[0].open()
    client.setHidden(true)
    vi.advanceTimersByTime(60_000)
    expect(FakeSocket.all[0].readyState).toBe(3)
    client.setHidden(false)
    expect(FakeSocket.all).toHaveLength(2)
    FakeSocket.all[1].open()
    expect(onOpen).toHaveBeenCalledTimes(2)
  })

  it('passes parsed messages through', () => {
    const { client, onMessage } = makeClient()
    client.start()
    FakeSocket.all[0].open()
    FakeSocket.all[0].onmessage?.(new MessageEvent('message', { data: '{"op":"error","message":"x"}' }))
    FakeSocket.all[0].onmessage?.(new MessageEvent('message', { data: 'not json' }))
    expect(onMessage).toHaveBeenCalledTimes(1)
    expect(onMessage).toHaveBeenCalledWith({ op: 'error', message: 'x' })
  })
})

describe('backoffDelay', () => {
  it('doubles from 1 s and caps at 30 s, with jitter', () => {
    expect([0, 1, 2, 3, 4, 5, 9].map((a) => backoffDelay(a, () => 1))).toEqual([1000, 2000, 4000, 8000, 16000, 30000, 30000])
    expect(backoffDelay(0, () => 0)).toBe(500)
  })
})

describe('wsUrl', () => {
  it('uses wss on https', () => {
    expect(wsUrl({ protocol: 'https:', host: 'a.dev' })).toBe('wss://a.dev/api/v1/ws')
    expect(wsUrl({ protocol: 'http:', host: 'localhost:5173' })).toBe('ws://localhost:5173/api/v1/ws')
  })
})

describe('cache updates and catch-up', () => {
  const event = (id: number): ApiEvent => ({
    id,
    task_id: 1,
    run_id: 1,
    seq: id,
    type: 'assistant_text',
    payload: { type: 'assistant_text', text: `e${id}`, ts: '2026-10-14T10:00:00Z', seq: id },
    created_at: '2026-10-14T10:00:00Z',
  })

  it('appends pushed events, deduplicated and in order', () => {
    const qc = new QueryClient()
    qc.setQueryData<EventsData>(keys.tasks.events(1), { items: [event(1), event(2)], local: [], older: '' })
    handleMessage(qc, { op: 'event', task_id: 1, event: event(4) })
    handleMessage(qc, { op: 'event', task_id: 1, event: event(2) })
    handleMessage(qc, { op: 'event', task_id: 1, event: event(3) })
    expect(qc.getQueryData<EventsData>(keys.tasks.events(1))!.items.map((e) => e.id)).toEqual([1, 2, 3, 4])
  })

  it('fetches missed events after reconnect with after_id = last cached id', async () => {
    const qc = new QueryClient()
    const { fetchEventsIncremental } = await import('@/api/hooks')
    qc.setQueryDefaults(keys.tasks.events(1), { queryFn: () => fetchEventsIncremental(qc, 1) })
    qc.setQueryData<EventsData>(keys.tasks.events(1), { items: [event(1), event(2)], local: [], older: '' })
    const calls: string[] = []
    const fetchSpy = vi.spyOn(globalThis, 'fetch').mockImplementation(async (input) => {
      const url = new URL((input as Request).url)
      calls.push(url.pathname + url.search)
      const after = Number(url.searchParams.get('after_id'))
      const items = after === 2 ? [event(3), event(4)] : []
      return new Response(JSON.stringify({ items, next_cursor: '' }), { headers: { 'Content-Type': 'application/json' } })
    })
    vi.useRealTimers()
    catchUp(qc, [1])
    await vi.waitFor(() => expect(qc.getQueryData<EventsData>(keys.tasks.events(1))!.items).toHaveLength(4))
    expect(calls[0]).toBe('/api/v1/tasks/1/events?after_id=2&limit=200')
    fetchSpy.mockRestore()
  })

  it('opens a timeline at its end with before_id=0 and keeps the older cursor', async () => {
    const qc = new QueryClient()
    const { fetchEventsIncremental } = await import('@/api/hooks')
    const calls: string[] = []
    const fetchSpy = vi.spyOn(globalThis, 'fetch').mockImplementation(async (input) => {
      const url = new URL((input as Request).url)
      calls.push(url.pathname + url.search)
      return new Response(JSON.stringify({ items: [event(9), event(8)], next_cursor: '8' }), {
        headers: { 'Content-Type': 'application/json' },
      })
    })
    vi.useRealTimers()
    const data = await fetchEventsIncremental(qc, 1)
    expect(calls).toEqual(['/api/v1/tasks/1/events?before_id=0&limit=200'])
    expect(data.items.map((e) => e.id)).toEqual([8, 9])
    expect(data.older).toBe('8')
    fetchSpy.mockRestore()
  })
})
