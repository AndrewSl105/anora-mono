import { expect, test } from '@playwright/test'

test.beforeEach(async ({ page }) => {
  await page.goto('/')
  await page.evaluate(() => localStorage.clear())
  await page.goto('/')
})

async function login(page: import('@playwright/test').Page) {
  await page.getByLabel('Password').fill('fleetpocket')
  await page.getByLabel('Authenticator code').fill('123456')
  await expect(page.getByRole('heading', { name: 'Tasks' })).toBeVisible()
}

test('login, create a task, answer a question', async ({ page }) => {
  await login(page)
  await expect(page.getByText('Retry webhook deliveries with backoff')).toBeVisible()

  // Create a task.
  await page.getByRole('link', { name: 'New' }).click()
  await page.getByLabel('What should Claude do?').fill('Add a health check endpoint\n\nReturn 200 with build info.')
  await page.getByRole('button', { name: 'Start task' }).click()
  await expect(page.getByRole('heading', { name: 'Add a health check endpoint' })).toBeVisible()
  await expect(page).toHaveURL(/\/tasks\/\d+$/)

  // Answer the open question on the waiting task.
  await page.goto('/tasks/5')
  await expect(page.getByText('How many attempts should a delivery get')).toBeVisible()
  await page.getByRole('button', { name: '5 attempts' }).click()
  await expect(page.getByText('Answered via web')).toBeVisible()
})

test('pick another agent for a new task', async ({ page }) => {
  await login(page)
  await page.getByRole('link', { name: 'New' }).click()
  // Mock mode has keys for Claude and Codex only.
  await page.getByLabel('Agent').click()
  await expect(page.getByRole('option', { name: 'Cursor' })).toHaveCount(0)
  await page.getByRole('option', { name: 'Codex' }).click()
  await page.getByLabel('What should Codex do?').fill('Rename the config loader')
  await page.getByRole('button', { name: 'Start task' }).click()
  await expect(page.getByRole('heading', { name: 'Rename the config loader' })).toBeVisible()
  await page.getByRole('tab', { name: 'Info' }).click()
  await expect(page.getByText('Codex', { exact: true })).toBeVisible()
})

test('wrong credentials show one generic message', async ({ page }) => {
  await page.getByLabel('Password').fill('nope')
  await page.getByLabel('Authenticator code').fill('123456')
  await expect(page.getByRole('alert')).toHaveText('Wrong password or code.')
})

test('deep link opens the task after login', async ({ page }) => {
  await login(page)
  await page.goto('/?task=1')
  await expect(page.getByRole('heading', { name: 'Add CSV export to Tables' })).toBeVisible()
})

test('long timelines open at the end and page backwards', async ({ page }) => {
  await login(page)
  await page.goto('/tasks/6')
  await expect(page.getByRole('heading', { name: 'Migrate every component to the new design tokens' })).toBeVisible()
  await expect(page.getByText('Moved 260 components to the design tokens.')).toBeVisible()
  const first = page.getByRole('button', { name: 'Edit · src/components/C001.tsx' })
  await expect(page.getByRole('button', { name: 'Edit · src/components/C260.tsx' })).toBeAttached()
  await expect(first).toHaveCount(0)
  await page.getByRole('button', { name: 'Load earlier activity' }).click()
  await page.getByRole('button', { name: 'Load earlier activity' }).click()
  await expect(page.getByText('Start of the task')).toBeVisible()
  // Prepending keeps the rows on screen in place; scroll up to the first ones.
  await page.locator('div[aria-live="polite"]').evaluate((el) => (el.scrollTop = 0))
  await expect(first).toBeVisible()
})

test('a message sent while running is queued; stopping the run starts it next', async ({ page }) => {
  await login(page)
  await page.goto('/tasks/7')
  await expect(page.getByRole('heading', { name: 'Paginate the audit log API' })).toBeVisible()
  await page.getByLabel('Message for the next run…').fill('Also document the cursor format')
  await page.getByRole('button', { name: 'Send', exact: true }).click()
  await expect(page.getByText('Queued: starts the next run when this one ends')).toBeVisible()
  await expect(page.getByText('Queued for the next run:')).toBeVisible()

  await page.getByRole('button', { name: 'Task actions' }).click()
  await page.getByRole('menuitem', { name: 'Stop and finish run' }).click()
  await expect(page.getByText('Stopping: the run ends with the work so far')).toBeVisible()
  await expect(page.getByText('Run started (followup)')).toBeVisible({ timeout: 20_000 })
  await expect(page.getByText('Queued: starts the next run when this one ends')).toHaveCount(0)
})
