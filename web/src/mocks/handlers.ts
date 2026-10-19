import { http, HttpResponse, ws, delay } from 'msw'
import type { ApiEvent, Project, Task } from '@/api/types'
import { GROUPS } from '@/lib/status'
import { addAttachment, addEvent, db, makeTask, startRun, taskView } from './db'
import { emitStatus, interruptRun, resumeAfterAnswer, setPush } from './simulator'

const SESSION_KEY = 'anora.mock.session'
const PASSWORD = 'anora'
let failures = 0

function session(): string | null {
  try {
    return localStorage.getItem(SESSION_KEY)
  } catch {
    return null
  }
}
function setSession(kind: string | null) {
  try {
    if (kind) localStorage.setItem(SESSION_KEY, kind)
    else localStorage.removeItem(SESSION_KEY)
  } catch {
    // ignore
  }
}

const err = (status: number, code: string, message: string, headers?: Record<string, string>) =>
  HttpResponse.json({ error: { code, message } }, { status, headers })

function authed() {
  return session() ? null : err(401, 'unauthorized', 'Not signed in')
}

function csrf(request: Request) {
  return request.headers.get('X-Requested-With') === 'anora' ? null : err(403, 'csrf', 'Missing X-Requested-With')
}

const IMAGE_TYPES = ['image/png', 'image/jpeg', 'image/webp', 'image/gif']
const MB = 1024 * 1024

type Body = { fields: Record<string, string | undefined>; files: File[] }

/** JSON or multipart (fields + files[]), validated like the backend (API.md, "Attachments"). */
async function readBody(request: Request, allowed: string[]): Promise<Body | { error: Response }> {
  if (!(request.headers.get('Content-Type') ?? '').startsWith('multipart/form-data')) {
    return { fields: (await request.json()) as Record<string, string | undefined>, files: [] }
  }
  const form = await request.formData()
  const fields: Record<string, string | undefined> = {}
  const files: File[] = []
  for (const [key, value] of form.entries()) {
    if (key === 'files[]' || key === 'files') {
      if (typeof value === 'string') return { error: err(400, 'invalid', 'files[] must be files') }
      files.push(value)
    } else if (allowed.includes(key)) fields[key] = String(value)
    else return { error: err(400, 'invalid', `unknown field "${key}"`) }
  }
  if (files.length > 10) return { error: err(400, 'invalid', 'at most 10 images per request') }
  if (files.reduce((n, f) => n + f.size, 0) > 100 * MB) return { error: err(400, 'invalid', 'the images are larger than 100 MB together') }
  for (const f of files) {
    if (!IMAGE_TYPES.includes(f.type)) return { error: err(400, 'invalid', `"${f.name}" is not a PNG, JPEG, WebP or GIF image`) }
    if (f.size > 40 * MB) return { error: err(400, 'invalid', `"${f.name}" is larger than 40 MB`) }
  }
  return { fields, files }
}

async function storeImages(taskId: number, files: File[]) {
  const out = []
  for (const f of files) {
    let width = 0
    let height = 0
    try {
      const bmp = await createImageBitmap(f)
      ;({ width, height } = bmp)
      bmp.close()
    } catch {
      // Dimensions are only cosmetic in mock mode.
    }
    out.push(addAttachment(taskId, f.name || 'image', f.type, new Uint8Array(await f.arrayBuffer()), width, height))
  }
  return out
}

function guard(request: Request) {
  return authed() ?? (request.method === 'GET' ? null : csrf(request))
}

function findTask(id: string | readonly string[] | undefined): Task | undefined {
  return db.tasks.find((t) => t.id === Number(id))
}

const API = '*/api/v1'

// ---- WebSocket ----

const socket = ws.link('*/api/v1/ws')
type Client = { send: (data: string) => void; subs: Set<string> }
const clients = new Set<Client>()

setPush({
  event: (taskId, event) => {
    for (const c of clients) if (c.subs.has(`task:${taskId}`)) c.send(JSON.stringify({ op: 'event', task_id: taskId, event }))
  },
  task: (task) => {
    for (const c of clients) {
      if (c.subs.has('scope:tasks') || c.subs.has(`task:${task.id}`)) c.send(JSON.stringify({ op: 'task', task }))
    }
  },
})

function pushEvent(taskId: number, event: ApiEvent) {
  for (const c of clients) if (c.subs.has(`task:${taskId}`)) c.send(JSON.stringify({ op: 'event', task_id: taskId, event }))
}
function pushTask(task: Task) {
  for (const c of clients) {
    if (c.subs.has('scope:tasks') || c.subs.has(`task:${task.id}`)) c.send(JSON.stringify({ op: 'task', task }))
  }
}

export const handlers = [
  socket.addEventListener('connection', ({ client }) => {
    if (!session()) {
      client.close(1008, 'unauthorized')
      return
    }
    const c: Client = { send: (d) => client.send(d), subs: new Set() }
    clients.add(c)
    client.addEventListener('message', (e) => {
      try {
        const msg = JSON.parse(String(e.data)) as { op: string; scope?: string; task_id?: number }
        const key = msg.scope ? `scope:${msg.scope}` : `task:${msg.task_id}`
        if (msg.op === 'subscribe') c.subs.add(key)
        if (msg.op === 'unsubscribe') c.subs.delete(key)
      } catch {
        client.send(JSON.stringify({ op: 'error', message: 'bad message' }))
      }
    })
    client.addEventListener('close', () => clients.delete(c))
  }),

  // ---- Auth ----
  http.post(`${API}/auth/login`, async ({ request }) => {
    await delay(300)
    if (failures >= 5) return err(429, 'rate_limited', 'Too many attempts', { 'Retry-After': '600' })
    const body = (await request.json()) as { password: string; totp: string }
    if (body.password !== PASSWORD || !/^\d{6}$/.test(body.totp) || body.totp === '000000') {
      failures++
      return err(401, 'bad_credentials', 'Wrong password or code')
    }
    failures = 0
    setSession('web')
    return HttpResponse.json({ ok: true, kind: 'web' })
  }),
  http.post(`${API}/auth/telegram`, async () => {
    await delay(400)
    setSession('telegram')
    return HttpResponse.json({ ok: true, kind: 'telegram', token: 'mock-telegram-token' })
  }),
  http.post(`${API}/ws/ticket`, ({ request }) => {
    const denied = guard(request)
    if (denied) return denied
    return HttpResponse.json({ ticket: crypto.randomUUID(), expires_in: 30 })
  }),
  http.post(`${API}/auth/logout`, () => {
    setSession(null)
    return HttpResponse.json({ ok: true })
  }),
  http.get(`${API}/me`, () => {
    const kind = session()
    return kind ? HttpResponse.json({ ok: true, kind: kind as 'web' | 'telegram' }) : err(401, 'unauthorized', 'Not signed in')
  }),

  // ---- Projects ----
  http.get(`${API}/projects`, ({ request }) => {
    const denied = guard(request)
    if (denied) return denied
    const archived = new URL(request.url).searchParams.get('archived') === 'true'
    return HttpResponse.json({ items: db.projects.filter((p) => archived || !p.archived), next_cursor: '' })
  }),
  http.post(`${API}/projects`, async ({ request }) => {
    const denied = guard(request)
    if (denied) return denied
    const body = (await request.json()) as Partial<Project> & { id: string }
    if (db.projects.some((p) => p.id === body.id)) return err(409, 'conflict', 'Project id exists')
    const p: Project = {
      instructions: '',
      services: [],
      env: {},
      runner_image: null,
      model: null,
      max_turns: null,
      max_budget_usd: null,
      max_duration_sec: null,
      runner_memory: null,
      runner_cpus: null,
      telegram_thread_id: null,
      archived: false,
      name: body.id,
      ...body,
      created_at: new Date().toISOString(),
      updated_at: new Date().toISOString(),
      repositories: [],
    }
    db.projects.push(p)
    return HttpResponse.json(p, { status: 201 })
  }),
  http.get(`${API}/projects/:id`, ({ request, params }) => {
    const denied = guard(request)
    if (denied) return denied
    const p = db.projects.find((x) => x.id === params.id)
    return p ? HttpResponse.json(p) : err(404, 'not_found', 'No such project')
  }),
  http.patch(`${API}/projects/:id`, async ({ request, params }) => {
    const denied = guard(request)
    if (denied) return denied
    const p = db.projects.find((x) => x.id === params.id)
    if (!p) return err(404, 'not_found', 'No such project')
    const body = (await request.json()) as Record<string, unknown>
    for (const [k, v] of Object.entries(body)) {
      ;(p as Record<string, unknown>)[k] = v === '' || v === 0 ? (k === 'name' || k === 'instructions' ? v : null) : v
    }
    p.updated_at = new Date().toISOString()
    return HttpResponse.json(p)
  }),
  http.delete(`${API}/projects/:id`, ({ request, params }) => {
    const denied = guard(request)
    if (denied) return denied
    const p = db.projects.find((x) => x.id === params.id)
    if (!p) return err(404, 'not_found', 'No such project')
    p.archived = true
    return HttpResponse.json(p)
  }),
  http.post(`${API}/projects/:id/repositories`, async ({ request, params }) => {
    const denied = guard(request)
    if (denied) return denied
    const p = db.projects.find((x) => x.id === params.id)
    if (!p) return err(404, 'not_found', 'No such project')
    const body = (await request.json()) as { full_name: string; installation_id: number; base_branch?: string; is_default?: boolean }
    const isDefault = body.is_default || p.repositories.length === 0
    if (isDefault) p.repositories.forEach((r) => (r.is_default = false))
    const r = { id: db.nextRepoId++, project_id: p.id, full_name: body.full_name, installation_id: body.installation_id, base_branch: body.base_branch ?? 'dev', is_default: isDefault }
    p.repositories.push(r)
    p.updated_at = new Date().toISOString()
    return HttpResponse.json(r, { status: 201 })
  }),
  http.patch(`${API}/projects/:id/repositories/:repoId`, async ({ request, params }) => {
    const denied = guard(request)
    if (denied) return denied
    const p = db.projects.find((x) => x.id === params.id)
    const r = p?.repositories.find((x) => x.id === Number(params.repoId))
    if (!p || !r) return err(404, 'not_found', 'No such repository')
    const body = (await request.json()) as { base_branch?: string; is_default?: boolean }
    if (body.is_default === false && r.is_default) return err(409, 'conflict', 'Make another repository the default instead')
    const unfinished = ['completed', 'failed', 'cancelled']
    if (body.base_branch !== undefined && body.base_branch !== r.base_branch && db.tasks.some((t) => t.repository_id === r.id && !unfinished.includes(t.status))) {
      return err(409, 'in_use', 'Unfinished tasks use this repository')
    }
    if (body.base_branch !== undefined) r.base_branch = body.base_branch
    if (body.is_default) p.repositories.forEach((x) => (x.is_default = x.id === r.id))
    p.updated_at = new Date().toISOString()
    return HttpResponse.json(r)
  }),
  http.delete(`${API}/projects/:id/repositories/:repoId`, ({ request, params }) => {
    const denied = guard(request)
    if (denied) return denied
    const p = db.projects.find((x) => x.id === params.id)
    if (!p) return err(404, 'not_found', 'No such project')
    const repoId = Number(params.repoId)
    if (db.tasks.some((t) => t.repository_id === repoId)) return err(409, 'in_use', 'Tasks reference this repository')
    p.repositories = p.repositories.filter((r) => r.id !== repoId)
    p.updated_at = new Date().toISOString()
    return HttpResponse.json({ ok: true })
  }),
  http.get(`${API}/github/repositories`, async ({ request }) => {
    const denied = guard(request)
    if (denied) return denied
    await delay(300)
    const names = ['vatary-server', 'vatary-web', 'anora-runner', 'anora-client', 'bioma', 'leadflow', 'streambox-nextjs']
    return HttpResponse.json({
      items: names.map((n, i) => ({ full_name: `andrewsl105/${n}`, default_branch: i % 2 ? 'main' : 'dev', private: i % 3 !== 0, installation_id: 123 })),
    })
  }),

  // ---- Tasks ----
  http.get(`${API}/tasks`, ({ request }) => {
    const denied = guard(request)
    if (denied) return denied
    const q = new URL(request.url).searchParams
    const statuses = q.get('status')?.split(',').filter(Boolean)
    const project = q.get('project')
    const items = db.tasks
      .filter((t) => (!statuses || statuses.includes(t.status)) && (!project || t.project_id === project))
      .sort((a, b) => b.created_at.localeCompare(a.created_at))
    return HttpResponse.json({ items, next_cursor: '' })
  }),
  http.post(`${API}/tasks`, async ({ request }) => {
    const denied = guard(request)
    if (denied) return denied
    const read = await readBody(request, ['project_id', 'prompt', 'repository_id', 'title'])
    if ('error' in read) return read.error
    const f = read.fields
    const body = { project_id: f.project_id, prompt: f.prompt ?? '', repository_id: f.repository_id ? Number(f.repository_id) : undefined, title: f.title }
    const p = db.projects.find((x) => x.id === body.project_id)
    if (!p) return err(404, 'not_found', 'No such project')
    if (!body.prompt?.trim()) return err(400, 'invalid', 'prompt is required')
    const task = makeTask({
      project_id: p.id,
      title: body.title || body.prompt.split('\n')[0].slice(0, 80),
      prompt: body.prompt,
      status: 'queued',
      repository_id: body.repository_id ?? p.repositories.find((r) => r.is_default)?.id ?? 1,
    })
    const attachments = await storeImages(task.id, read.files)
    addEvent(task.id, 'created', { attachments })
    startRun(task.id, 'initial', task.prompt)
    addEvent(task.id, 'status', { status: 'queued' })
    pushTask(task)
    return HttpResponse.json(taskView(task), { status: 201 })
  }),
  http.get(`${API}/tasks/:id`, ({ request, params }) => {
    const denied = guard(request)
    if (denied) return denied
    const task = findTask(params.id)
    return task ? HttpResponse.json(taskView(task)) : err(404, 'not_found', 'No such task')
  }),
  http.get(`${API}/tasks/:id/events`, async ({ request, params }) => {
    const denied = guard(request)
    if (denied) return denied
    const task = findTask(params.id)
    if (!task) return err(404, 'not_found', 'No such task')
    await delay(150)
    const q = new URL(request.url).searchParams
    const limit = Math.min(200, Number(q.get('limit') ?? 50))
    const before = q.get('before_id')
    if (before !== null && (q.has('after_id') || q.has('cursor'))) return err(400, 'invalid', 'before_id cannot be combined with after_id')
    // before_id pages backwards, newest first; 0 = from the newest event.
    const all =
      before !== null
        ? (db.events.get(task.id) ?? []).filter((e) => Number(before) === 0 || e.id < Number(before)).reverse()
        : (db.events.get(task.id) ?? []).filter((e) => e.id > Number(q.get('after_id') ?? q.get('cursor') ?? 0))
    const items = all.slice(0, limit)
    return HttpResponse.json({ items, next_cursor: all.length > limit ? String(items[items.length - 1].id) : '' })
  }),
  http.get(`${API}/tasks/:id/attachments/:aid`, async ({ request, params }) => {
    const denied = guard(request)
    if (denied) return denied
    const a = db.attachments.get(Number(params.aid))
    if (!a || a.taskId !== Number(params.id)) return err(404, 'not_found', 'No such attachment')
    if (a.deleted) return err(410, 'gone', 'the attachment was removed')
    await delay(200)
    return new HttpResponse(a.bytes.slice().buffer, { headers: { 'Content-Type': a.meta.mime, 'Cache-Control': 'private' } })
  }),
  http.get(`${API}/tasks/:id/runs`, ({ request, params }) => {
    const denied = guard(request)
    if (denied) return denied
    const task = findTask(params.id)
    if (!task) return err(404, 'not_found', 'No such task')
    return HttpResponse.json({ items: db.runs.get(task.id) ?? [], next_cursor: '' })
  }),
  http.get(`${API}/tasks/:id/diff`, async ({ request, params }) => {
    const denied = guard(request)
    if (denied) return denied
    const task = findTask(params.id)
    if (!task) return err(404, 'not_found', 'No such task')
    if (task.status === 'queued' || task.status === 'completed' || task.status === 'cancelled') {
      return err(409, 'no_workspace', 'No workspace')
    }
    await delay(200)
    return HttpResponse.json({ diff: SAMPLE_DIFF, truncated: false, base: 'dev', branch: task.branch ?? '' })
  }),
  http.post(`${API}/tasks/:id/answer`, async ({ request, params }) => {
    const denied = guard(request)
    if (denied) return denied
    const task = findTask(params.id)
    if (!task) return err(404, 'not_found', 'No such task')
    const body = (await request.json()) as { question_id: string; text: string }
    const q = (db.questions.get(task.id) ?? []).find((x) => x.id === body.question_id)
    if (!q) return err(404, 'not_found', 'No such question')
    if (q.answer) return err(409, 'already_answered', 'Already answered')
    await delay(300)
    Object.assign(q, { answer: body.text, answered_via: 'web', answered_at: new Date().toISOString() })
    pushEvent(task.id, addEvent(task.id, 'answer', { question_id: q.id, text: body.text, answered_via: 'web' }))
    resumeAfterAnswer(task)
    return HttpResponse.json(taskView(task))
  }),
  http.post(`${API}/tasks/:id/message`, async ({ request, params }) => {
    const denied = guard(request)
    if (denied) return denied
    const task = findTask(params.id)
    if (!task) return err(404, 'not_found', 'No such task')
    const read = await readBody(request, ['text'])
    if ('error' in read) return read.error
    const body = { text: read.fields.text ?? '' }
    if (!body.text.trim()) return err(400, 'invalid', 'text is required')
    const queue = ['queued', 'preparing', 'running', 'waiting_input', 'pushing'].includes(task.status)
    const followup = ['paused', 'pr_open', 'completed'].includes(task.status)
    if (!queue && !followup) return err(409, 'conflict', 'Not allowed in this state')
    await delay(250)
    const attachments = await storeImages(task.id, read.files)
    pushEvent(
      task.id,
      addEvent(task.id, 'message', { text: body.text, delivery: queue ? 'queued' : 'new_run', ...(attachments.length ? { attachments } : {}) }),
    )
    if (queue) {
      task.queued_message = task.queued_message ? `${task.queued_message}\n\n${body.text}` : body.text
      pushTask(task)
    }
    if (followup) {
      const run = startRun(task.id, 'followup', body.text)
      pushEvent(task.id, (db.events.get(task.id) ?? []).find((e) => e.type === 'run_started' && (e.payload as unknown as { run_id: number }).run_id === run.id)!)
      emitStatus(task, 'queued')
    }
    return HttpResponse.json(taskView(task))
  }),
  http.post(`${API}/tasks/:id/interrupt`, ({ request, params }) => {
    const denied = guard(request)
    if (denied) return denied
    const task = findTask(params.id)
    if (!task) return err(404, 'not_found', 'No such task')
    if (!['running', 'waiting_input'].includes(task.status)) return err(409, 'not_running', 'Not running')
    pushEvent(task.id, addEvent(task.id, 'interrupt', {}))
    // Like the backend: the run ends as done ("Interrupted by the user.") and pushes what it has.
    interruptRun(task)
    return HttpResponse.json(taskView(task))
  }),
  http.post(`${API}/tasks/:id/cancel`, ({ request, params }) => {
    const denied = guard(request)
    if (denied) return denied
    const task = findTask(params.id)
    if (!task) return err(404, 'not_found', 'No such task')
    if (!GROUPS.active.includes(task.status) && task.status !== 'pr_open') return err(409, 'conflict', 'Not allowed')
    emitStatus(task, 'cancelled', 'cancelled by the user')
    return HttpResponse.json(taskView(task))
  }),
  http.post(`${API}/tasks/:id/retry`, ({ request, params }) => {
    const denied = guard(request)
    if (denied) return denied
    const task = findTask(params.id)
    if (!task) return err(404, 'not_found', 'No such task')
    if (task.status !== 'failed' && task.status !== 'cancelled') return err(409, 'conflict', 'Not allowed')
    startRun(task.id, 'retry', task.prompt)
    task.finished_at = null
    emitStatus(task, 'queued')
    return HttpResponse.json(taskView(task))
  }),

  http.post(`${API}/tasks/:id/merge`, ({ request, params }) => {
    const denied = guard(request)
    if (denied) return denied
    const task = findTask(params.id)
    if (!task) return err(404, 'not_found', 'No such task')
    if (task.status !== 'pr_open' || task.pr_number == null) return err(409, 'conflict', 'Not allowed')
    pushEvent(task.id, addEvent(task.id, 'pr_closed', { number: task.pr_number, merged: true }))
    emitStatus(task, 'completed', 'PR merged')
    return HttpResponse.json(taskView(task))
  }),

  // ---- System ----
  http.get(`${API}/stats`, ({ request }) => {
    const denied = guard(request)
    if (denied) return denied
    const count = (s: Task['status']) => db.tasks.filter((t) => t.status === s).length
    return HttpResponse.json({
      running: count('running'),
      queued: count('queued'),
      waiting_input: count('waiting_input'),
      cost_usd_today: 2.54,
      cost_usd_month: 41.7,
    })
  }),
  http.get('*/healthz', () => HttpResponse.json({ ok: true, checks: { database: 'ok', docker: 'ok' } })),
]

const SAMPLE_DIFF = `diff --git a/src/audit/routes.ts b/src/audit/routes.ts
index 3b18e51..a9c2f0d 100644
--- a/src/audit/routes.ts
+++ b/src/audit/routes.ts
@@ -12,14 +12,24 @@ import { auditRepository } from './repository'
 export const auditRoutes = router()

 auditRoutes.get('/audit', async (req, res) => {
-  const rows = await auditRepository.all()
-  res.json({ items: rows })
+  const limit = Math.min(Number(req.query.limit ?? 50), 200)
+  const cursor = decodeCursor(req.query.cursor)
+  const rows = await auditRepository.page({ limit: limit + 1, after: cursor })
+  const items = rows.slice(0, limit)
+  const next = rows.length > limit ? encodeCursor(items[items.length - 1]) : ''
+  res.json({ items, next_cursor: next })
 })
+
+function encodeCursor(row: { created_at: Date; id: number }) {
+  return Buffer.from(\`\${row.created_at.toISOString()}|\${row.id}\`).toString('base64url')
+}
diff --git a/src/audit/repository.ts b/src/audit/repository.ts
index 77de102..1f0aa3b 100644
--- a/src/audit/repository.ts
+++ b/src/audit/repository.ts
@@ -4,8 +4,14 @@ export const auditRepository = {
-  all() {
-    return db.select().from(audit).orderBy(desc(audit.createdAt))
+  page({ limit, after }: { limit: number; after?: Cursor }) {
+    const q = db.select().from(audit)
+    if (after) q.where(sql\`(created_at, id) < (\${after.createdAt}, \${after.id})\`)
+    return q.orderBy(desc(audit.createdAt), desc(audit.id)).limit(limit)
   },
 }
`
