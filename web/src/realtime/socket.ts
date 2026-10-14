import type { ApiEvent, Task } from '@/api/types'

export type ServerMessage =
  | { op: 'event'; task_id: number; event: ApiEvent }
  | { op: 'task'; task: Task }
  | { op: 'error'; message: string }

export type Subscription = { scope: 'tasks' } | { task_id: number }
export type SocketStatus = 'idle' | 'connecting' | 'open' | 'closed'

type SocketLike = {
  send(data: string): void
  close(): void
  readonly readyState: number
  onopen: ((ev: Event) => void) | null
  onclose: ((ev: CloseEvent) => void) | null
  onmessage: ((ev: MessageEvent) => void) | null
  onerror: ((ev: Event) => void) | null
}

export type RealtimeOptions = {
  url: () => string
  onMessage: (msg: ServerMessage) => void
  /** Called on every successful (re)connect, after subscriptions were re-sent. */
  onOpen: (taskIds: number[]) => void
  createSocket?: (url: string) => SocketLike
  random?: () => number
  backgroundCloseMs?: number
}

const MIN_DELAY = 1_000
const MAX_DELAY = 30_000
const OPEN = 1

export function subKey(sub: Subscription): string {
  return 'scope' in sub ? `scope:${sub.scope}` : `task:${sub.task_id}`
}

/** Exponential backoff 1 s → 30 s with jitter (50–100 % of the step). */
export function backoffDelay(attempt: number, random: () => number = Math.random): number {
  const step = Math.min(MAX_DELAY, MIN_DELAY * 2 ** attempt)
  return Math.round(step * (0.5 + random() * 0.5))
}

/** One connection per app session with reference-counted subscriptions. */
export class RealtimeClient {
  private socket?: SocketLike
  private subs = new Map<string, { sub: Subscription; count: number }>()
  private status: SocketStatus = 'idle'
  private listeners = new Set<() => void>()
  private attempt = 0
  private retryTimer?: ReturnType<typeof setTimeout>
  private hiddenTimer?: ReturnType<typeof setTimeout>
  private running = false
  private suspended = false
  private readonly opts: RealtimeOptions

  constructor(opts: RealtimeOptions) {
    this.opts = opts
  }

  start() {
    if (this.running) return
    this.running = true
    this.connect()
  }

  stop() {
    this.running = false
    clearTimeout(this.retryTimer)
    clearTimeout(this.hiddenTimer)
    this.closeSocket()
    this.setStatus('idle')
  }

  subscribe(sub: Subscription): () => void {
    const key = subKey(sub)
    const entry = this.subs.get(key)
    if (entry) entry.count++
    else {
      this.subs.set(key, { sub, count: 1 })
      this.send({ op: 'subscribe', ...sub })
    }
    let released = false
    return () => {
      if (released) return
      released = true
      const current = this.subs.get(key)
      if (!current) return
      if (--current.count === 0) {
        this.subs.delete(key)
        this.send({ op: 'unsubscribe', ...sub })
      }
    }
  }

  taskIds(): number[] {
    return [...this.subs.values()].flatMap(({ sub }) => ('task_id' in sub ? [sub.task_id] : []))
  }

  getStatus = (): SocketStatus => this.status

  onStatus = (listener: () => void): (() => void) => {
    this.listeners.add(listener)
    return () => this.listeners.delete(listener)
  }

  /** Close while the Mini App is in the background for long; reconnect and catch up on return. */
  setHidden(hidden: boolean) {
    clearTimeout(this.hiddenTimer)
    if (hidden) {
      this.hiddenTimer = setTimeout(() => {
        this.suspended = true
        clearTimeout(this.retryTimer)
        this.closeSocket()
        this.setStatus('idle')
      }, this.opts.backgroundCloseMs ?? 60_000)
    } else if (this.suspended && this.running) {
      this.suspended = false
      this.attempt = 0
      this.connect()
    }
  }

  private connect() {
    if (!this.running || this.suspended) return
    this.setStatus('connecting')
    let socket: SocketLike
    try {
      socket = (this.opts.createSocket ?? ((url) => new WebSocket(url)))(this.opts.url())
    } catch {
      this.scheduleReconnect()
      return
    }
    this.socket = socket
    socket.onopen = () => {
      this.attempt = 0
      this.setStatus('open')
      for (const { sub } of this.subs.values()) this.send({ op: 'subscribe', ...sub })
      this.opts.onOpen(this.taskIds())
    }
    socket.onmessage = (ev) => {
      try {
        this.opts.onMessage(JSON.parse(String(ev.data)) as ServerMessage)
      } catch {
        // ignore malformed frames
      }
    }
    socket.onclose = () => {
      if (this.socket !== socket) return
      this.socket = undefined
      if (!this.running || this.suspended) return
      this.setStatus('closed')
      this.scheduleReconnect()
    }
    socket.onerror = () => {
      // onclose follows and handles the reconnect.
    }
  }

  private scheduleReconnect() {
    clearTimeout(this.retryTimer)
    this.setStatus('closed')
    const delay = backoffDelay(this.attempt++, this.opts.random)
    this.retryTimer = setTimeout(() => this.connect(), delay)
  }

  private closeSocket() {
    const socket = this.socket
    this.socket = undefined
    if (socket) {
      socket.onclose = null
      socket.onmessage = null
      try {
        socket.close()
      } catch {
        // already closed
      }
    }
  }

  private send(msg: object) {
    if (this.socket && this.socket.readyState === OPEN) this.socket.send(JSON.stringify(msg))
  }

  private setStatus(status: SocketStatus) {
    if (this.status === status) return
    this.status = status
    for (const l of this.listeners) l()
  }
}

export function wsUrl(location: Pick<Location, 'protocol' | 'host'> = window.location): string {
  return `${location.protocol === 'https:' ? 'wss' : 'ws'}://${location.host}/api/v1/ws`
}
