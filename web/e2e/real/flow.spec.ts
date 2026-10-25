import { expect, test, type Page } from '@playwright/test'
import { totp } from './totp'

// Runs against a real Fleetpocket with the fake runner (FLEETPOCKET_RUNNER_FAKE): every
// run asks "Which greeting should I write?" (hello / hi), commits and finishes.
// Without a GitHub App the task ends "completed" (committed locally).

const password = process.env.E2E_PASSWORD ?? ''
const secret = process.env.E2E_TOTP_SECRET ?? ''
// A public repository the backend can clone anonymously, and its branch.
const repo = process.env.E2E_REPO ?? 'octocat/Hello-World'
const branch = process.env.E2E_REPO_BRANCH ?? 'master'
const QUESTION = 'Which greeting should I write?'

test.skip(!password || !secret, 'set E2E_PASSWORD and E2E_TOTP_SECRET')

// Wrong credentials are tested in mock mode only: the backend allows 5 failed logins
// per IP per 15 minutes, so repeated runs would lock the next ones out.

// Contract mismatches often show up only as a failed request or a console
// error the UI swallows: collect them and fail the test on any.
const problems = new WeakMap<Page, string[]>()
test.beforeEach(async ({ page }) => {
  const list: string[] = []
  problems.set(page, list)
  page.on('console', (m) => {
    // "Failed to load resource" repeats a response; responses are checked below with their URL.
    if (m.type() === 'error' && !m.text().startsWith('Failed to load resource')) list.push(`console: ${m.text()}`)
  })
  page.on('pageerror', (e) => list.push(`pageerror: ${e.message}`))
  page.on('response', (r) => {
    const url = new URL(r.url())
    // The session check before login is expected to be 401.
    if (r.status() >= 400 && !(url.pathname === '/api/v1/me' && r.status() === 401)) {
      list.push(`${r.status()} ${r.request().method()} ${url.pathname}${url.search}`)
    }
  })
  page.on('websocket', (ws) => ws.on('socketerror', (e) => list.push(`websocket: ${e}`)))
})
test.afterEach(async ({ page }, info) => {
  const list = problems.get(page) ?? []
  const allowed = info.annotations.find((a) => a.type === 'allow')?.description
  const unexpected = allowed ? list.filter((p) => !p.includes(allowed)) : list
  expect(unexpected, 'unexpected errors during the test').toEqual([])
})

async function login(page: Page) {
  await page.goto('/')
  await page.getByLabel('Password').fill(password)
  await page.getByLabel('Authenticator code').fill(totp(secret))
  await expect(page.getByRole('heading', { name: 'Tasks' })).toBeVisible()
}

/** Creates a project in the UI and adds the repository through the API (no GitHub App in dev). */
async function createProject(page: Page): Promise<string> {
  const id = `e2e-${Date.now().toString(36)}`
  await page.goto('/projects/new')
  await page.getByLabel('Id (slug)').fill(id)
  await page.getByLabel('Name').fill(`E2E ${id}`)
  await page.getByRole('button', { name: 'New project' }).click()
  await expect(page.getByText('Project created')).toBeVisible()
  const res = await page.request.post(`/api/v1/projects/${id}/repositories`, {
    headers: { 'X-Requested-With': 'fleetpocket' },
    data: { full_name: repo, installation_id: 1, base_branch: branch },
  })
  expect(res.status(), await res.text()).toBe(201)
  return id
}

// Fake runs that wait for an answer hold a concurrency slot: cancel what a test leaves open.
const created: number[] = []
test.afterEach(async ({ page }) => {
  for (const id of created.splice(0)) {
    await page.request.post(`/api/v1/tasks/${id}/cancel`, { headers: { 'X-Requested-With': 'fleetpocket' } })
  }
})

async function createTask(page: Page, project: string, prompt: string) {
  await page.goto('/new')
  await page.getByRole('combobox', { name: 'Project' }).click()
  await page.getByRole('option', { name: `E2E ${project}` }).click()
  await page.getByLabel('What should Claude do?').fill(prompt)
  await page.getByRole('button', { name: 'Start task' }).click()
  await expect(page).toHaveURL(/\/tasks\/\d+$/)
  created.push(Number(page.url().split('/').pop()))
}

async function answer(page: Page, option: string) {
  const card = page.locator('section, div').filter({ hasText: QUESTION }).last()
  await expect(page.getByText(QUESTION).last()).toBeVisible({ timeout: 90_000 })
  await card.getByRole('button', { name: option, exact: true }).click()
  await expect(page.getByText('Answered via web').last()).toBeVisible()
}

test('create a project and a task, answer the question, the run finishes', async ({ page }) => {
  await login(page)
  const project = await createProject(page)
  await createTask(page, project, 'Write a greeting file')
  await expect(page.getByRole('heading', { name: 'Write a greeting file' })).toBeVisible()

  // Live timeline: the run starts and the fake runner asks its question.
  await expect(page.getByText('Run started (initial)')).toBeVisible({ timeout: 90_000 })
  await answer(page, 'hello')
  await expect(page.getByText(/Committed 1 file/)).toBeVisible({ timeout: 60_000 })
  await expect(page.getByText('Completed').first()).toBeVisible({ timeout: 60_000 })

  // Info tab reads GET /tasks/{id}/runs.
  await page.getByRole('tab', { name: 'Info' }).click()
  await expect(page.getByRole('cell', { name: 'initial' })).toBeVisible()
})

test('a message sent while the run is active is queued and starts the next run', async ({ page }) => {
  await login(page)
  const project = await createProject(page)
  await createTask(page, project, 'Greeting with a follow-up')
  await expect(page.getByText(QUESTION).last()).toBeVisible({ timeout: 90_000 })

  await page.getByLabel('Message for the next run…').fill('Also add a footer')
  await page.getByRole('button', { name: 'Send', exact: true }).click()
  await expect(page.getByText('Queued for the next run:')).toBeVisible()
  await expect(page.getByText('Queued: starts the next run when this one ends')).toBeVisible()

  await answer(page, 'hello')
  // The run ends, the queued text starts a follow-up run, which asks again.
  await expect(page.getByText('Run started (followup)')).toBeVisible({ timeout: 90_000 })
  await expect(page.getByText('Queued: starts the next run when this one ends')).toHaveCount(0)
  await answer(page, 'hi')
  await expect(page.getByText('Completed').first()).toBeVisible({ timeout: 60_000 })

  await page.getByRole('tab', { name: 'Info' }).click()
  await expect(page.getByRole('cell', { name: 'followup' })).toBeVisible()
})


test('deep link opens the task after login', async ({ page }) => {
  await login(page)
  const project = await createProject(page)
  await createTask(page, project, 'Deep link target')
  const id = page.url().split('/').pop()
  await page.goto('/')
  await page.goto(`/?task=${id}`)
  await expect(page.getByRole('heading', { name: 'Deep link target' })).toBeVisible()
  await answer(page, 'hi')
})

