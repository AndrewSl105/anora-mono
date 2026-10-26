import { fileURLToPath } from 'node:url'
import { expect, test, type Page } from '@playwright/test'

// README screenshots and the demo video, in mock mode with the seeded data from
// src/mocks/db.ts. Run with `npm run screenshots` (playwright.screenshots.config.ts).
// Date is frozen, so relative times are the same every time; timers run normally, and the
// running task is captured right after a known step of the mock simulator.

const OUT = fileURLToPath(new URL('../../../docs/screenshots/', import.meta.url))
const NOW = new Date('2026-10-20T09:30:00Z')

// Toasts, the reconnect banner and the debug panel are not part of the screens.
const HIDE = `
  [data-sonner-toaster], [role="status"].bg-amber-500\\/15 { display: none !important; }
  * { caret-color: transparent !important; }
`

async function hideOverlays(page: Page) {
  await page.addInitScript({
    content: `document.addEventListener('DOMContentLoaded', () => {
      const style = document.createElement('style')
      style.textContent = ${JSON.stringify(HIDE)}
      document.head.appendChild(style)
    })`,
  })
}

const isDesktop = () => test.info().project.name.startsWith('desktop')

async function open(page: Page, path: string) {
  await page.clock.setFixedTime(NOW)
  await hideOverlays(page)
  await page.goto(path)
}

async function login(page: Page) {
  await page.getByLabel('Password').fill('fleetpocket')
  await page.getByLabel('Authenticator code').fill('123456')
  await expect(page.getByRole('heading', { name: 'Tasks' })).toBeVisible()
}

async function shot(page: Page, screen: string) {
  const [device, theme] = test.info().project.name.split('-')
  // Attachment thumbnails load after the timeline renders.
  await page.waitForFunction('Array.from(document.images).every((img) => img.complete && img.naturalWidth > 0)')
  await page.screenshot({ path: `${OUT}${screen}-${device}-${theme}.png`, animations: 'disabled', caret: 'hide' })
}

test.describe('screens', () => {
  test('login', async ({ page }) => {
    test.skip(isDesktop())
    await open(page, '/')
    await expect(page.getByRole('heading', { name: 'Sign in to Fleetpocket' })).toBeVisible()
    await shot(page, 'login')
  })

  test('task list', async ({ page }) => {
    await open(page, '/')
    await login(page)
    await expect(page.getByText('Retry webhook deliveries with backoff')).toBeVisible()
    await expect(page.getByText('Paginate the audit log API')).toBeVisible()
    await expect(page.getByText('Rate-limit the public search endpoint')).toBeVisible()
    await shot(page, 'tasks')
  })

  test('running task with a live timeline', async ({ page }) => {
    await open(page, '/')
    await login(page)
    await page.goto('/tasks/7')
    await expect(page.getByRole('heading', { name: 'Paginate the audit log API' })).toBeVisible()
    // The simulator adds one event every 2.5 s; capture right after its sixth step
    // (a search, the plan and two edits), before the seventh arrives.
    await expect(page.getByRole('button', { name: 'Edit · src/audit/repository.ts' })).toBeVisible({ timeout: 20_000 })
    await expect(page.getByText('keyset pagination')).toBeVisible()
    await shot(page, 'task-running')
  })

  test('task waiting on a question', async ({ page }) => {
    test.skip(isDesktop())
    await open(page, '/')
    await login(page)
    await page.goto('/tasks/5')
    await expect(page.getByText('How many attempts should a delivery get')).toBeVisible()
    await expect(page.getByRole('button', { name: '5 attempts' })).toBeVisible()
    await shot(page, 'task-question')
  })

  test('task with an open PR', async ({ page }) => {
    test.skip(isDesktop())
    await open(page, '/')
    await login(page)
    await page.goto('/tasks/1')
    await expect(page.getByRole('heading', { name: 'Add CSV export to Tables' })).toBeVisible()
    await expect(page.getByText('streams rows with proper quoting')).toBeVisible()
    await shot(page, 'task-pr')
  })

  test('new task', async ({ page }) => {
    test.skip(isDesktop())
    await open(page, '/')
    await login(page)
    await page.getByRole('link', { name: 'New' }).click()
    await page
      .getByLabel('What should Claude do?')
      .fill('Add a /healthz endpoint to the API\n\nReturn 200 with the build version and check the database connection. Add a test.')
    await expect(page.getByRole('button', { name: 'Start task' })).toBeVisible()
    await shot(page, 'new-task')
  })

  test('projects', async ({ page }) => {
    test.skip(isDesktop())
    await open(page, '/')
    await login(page)
    await page.getByRole('link', { name: 'Projects' }).click()
    await expect(page.getByRole('heading', { name: 'Projects' })).toBeVisible()
    await expect(page.getByText('Vatary', { exact: true })).toBeVisible()
    await shot(page, 'projects')
  })

  test('settings', async ({ page }) => {
    test.skip(isDesktop())
    await open(page, '/')
    await login(page)
    await page.getByRole('link', { name: 'Settings' }).click()
    await expect(page.getByRole('heading', { name: 'Settings' })).toBeVisible()
    await expect(page.getByText('Healthy')).toBeVisible()
    await shot(page, 'settings')
  })
})

// login → create a task → answer a question, recorded as a video; scripts/demo-gif.sh
// turns it into docs/screenshots/demo.gif.
test('demo video', async ({ browser }) => {
  test.skip(test.info().project.name !== 'phone-light')
  const size = { width: 390, height: 844 }
  const context = await browser.newContext({
    viewport: size,
    isMobile: true,
    hasTouch: true,
    colorScheme: 'light',
    recordVideo: { dir: test.info().outputPath('video'), size },
  })
  const page = await context.newPage()
  await page.clock.setFixedTime(NOW)
  await hideOverlays(page)
  await page.goto('/')
  await expect(page.getByRole('heading', { name: 'Sign in to Fleetpocket' })).toBeVisible()
  await page.waitForTimeout(600)
  await page.getByLabel('Password').pressSequentially('fleetpocket', { delay: 80 })
  await page.getByLabel('Authenticator code').pressSequentially('123456', { delay: 80 })
  await expect(page.getByText('Retry webhook deliveries with backoff')).toBeVisible()
  await page.waitForTimeout(1200)

  await page.getByRole('link', { name: 'New' }).click()
  await page.getByLabel('What should Claude do?').pressSequentially('Add a /healthz endpoint that returns the build version', { delay: 35 })
  await page.waitForTimeout(500)
  await page.getByRole('button', { name: 'Start task' }).click()
  await expect(page.getByRole('heading', { name: 'Add a /healthz endpoint that returns the build version' })).toBeVisible()
  await page.waitForTimeout(3000)

  await page.getByRole('button', { name: 'Back' }).click()
  await page.getByText('Retry webhook deliveries with backoff').click()
  await expect(page.getByText('How many attempts should a delivery get')).toBeVisible()
  await page.waitForTimeout(1200)
  await page.getByRole('button', { name: '5 attempts' }).click()
  await expect(page.getByText('Answered via web')).toBeVisible()
  await page.waitForTimeout(2000)
  await context.close()
  await page.video()!.saveAs(`${test.info().outputDir}/../demo.webm`)
})
