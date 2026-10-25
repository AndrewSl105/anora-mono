import { createHmac } from 'node:crypto'
import { createServer } from 'node:http'
import { expect, test } from '@playwright/test'

// Telegram Web (web.telegram.org) shows the Mini App in a cross-site iframe,
// where the SameSite=Lax session cookie is never sent. This test reproduces
// that: a page on another site (127.0.0.1) embeds the app (localhost) with
// signed initData in the URL hash, like Telegram does. The app must work
// through the bearer token and a WebSocket ticket.
// Needs the backend's TELEGRAM_BOT_TOKEN and TELEGRAM_ALLOWED_USER_ID.

const botToken = process.env.E2E_TG_BOT_TOKEN ?? ''
const userId = Number(process.env.E2E_TG_USER_ID ?? 0)
test.skip(!botToken || !userId, 'set E2E_TG_BOT_TOKEN and E2E_TG_USER_ID')

/** initData signed like Telegram does (core.telegram.org/bots/webapps#validating-data-received-via-the-mini-app). */
function initData(): string {
  const fields: Record<string, string> = {
    auth_date: String(Math.floor(Date.now() / 1000)),
    query_id: 'AAE2E',
    user: JSON.stringify({ id: userId, first_name: 'E2E' }),
  }
  const check = Object.keys(fields)
    .sort()
    .map((k) => `${k}=${fields[k]}`)
    .join('\n')
  const secret = createHmac('sha256', 'WebAppData').update(botToken).digest()
  const hash = createHmac('sha256', secret).update(check).digest('hex')
  return new URLSearchParams({ ...fields, hash }).toString()
}

test('the Mini App in a cross-site iframe works without the cookie', async ({ page, baseURL }) => {
  const appOrigin = new URL(baseURL!).origin
  const hash = new URLSearchParams({ tgWebAppData: initData(), tgWebAppVersion: '8.0', tgWebAppPlatform: 'weba' })
  // A real loopback server: Chromium's Local Network Access checks block a
  // routed (address-less, i.e. public) page from framing localhost.
  const server = createServer((_req, res) => {
    res.setHeader('Content-Type', 'text/html')
    res.end(`<!doctype html><iframe id="app" src="${appOrigin}/#${hash}" style="width:400px;height:800px;border:0"></iframe>`)
  })
  await new Promise<void>((resolve) => server.listen(5199, '127.0.0.1', resolve))
  try {
    const parent = 'http://127.0.0.1:5199/'

    const api: { path: string; auth: string | null; cookie: string | null }[] = []
    page.on('request', async (r) => {
      const url = new URL(r.url())
      if (url.origin !== appOrigin || !url.pathname.startsWith('/api/')) return
      const h = await r.allHeaders()
      api.push({ path: url.pathname, auth: h['authorization'] ?? null, cookie: h['cookie'] ?? null })
    })
    const sockets: string[] = []
    const pushes: string[] = []
    page.on('websocket', (ws) => {
      sockets.push(ws.url())
      ws.on('framereceived', (f) => pushes.push(String(f.payload)))
    })

    await page.goto(parent)
    const app = page.frameLocator('#app')
    await expect(app.getByRole('heading', { name: 'Tasks' })).toBeVisible({ timeout: 30_000 })

    // REST: authenticated by the bearer token; the Lax cookie never reaches the iframe.
    await expect.poll(() => api.some((a) => a.path === '/api/v1/tasks')).toBe(true)
    const afterLogin = api.filter((a) => a.path !== '/api/v1/auth/telegram')
    expect(afterLogin.length).toBeGreaterThan(0)
    for (const a of afterLogin) {
      expect(a.auth, a.path).toMatch(/^Bearer /)
      expect(a.cookie, a.path).toBeNull()
    }

    // WebSocket: opened with a single-use ticket, and it receives pushes.
    await expect.poll(() => sockets.some((u) => u.includes('/api/v1/ws?ticket='))).toBe(true)
    const res = await page.request.post(`${appOrigin}/api/v1/auth/telegram`, {
      headers: { 'X-Requested-With': 'fleetpocket' },
      data: { init_data: initData() },
    })
    const { token } = (await res.json()) as { token: string }
    const projects = await page.request.get(`${appOrigin}/api/v1/projects`, { headers: { Authorization: `Bearer ${token}` } })
    const project = ((await projects.json()) as { items: { id: string }[] }).items[0]
    test.skip(!project, 'needs a project with a repository (run flow.spec.ts first)')
    const auth = { 'X-Requested-With': 'fleetpocket', Authorization: `Bearer ${token}` }
    const task = (await (
      await page.request.post(`${appOrigin}/api/v1/tasks`, {
        headers: auth,
        data: { project_id: project.id, prompt: 'Created while the iframe listens' },
      })
    ).json()) as { id: number }
    try {
      await expect(app.getByText('Created while the iframe listens')).toBeVisible({ timeout: 30_000 })
      expect(pushes.some((p) => p.includes('"op":"task"'))).toBe(true)
    } finally {
      // Its fake run would hold a concurrency slot while waiting for an answer.
      await page.request.post(`${appOrigin}/api/v1/tasks/${task.id}/cancel`, { headers: auth })
    }
  } finally {
    server.close()
  }
})
