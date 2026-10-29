import { devices, expect, test } from '@playwright/test'

// The canvas is a desktop view (fine pointer, wide screen).
test.use({ ...devices['Desktop Chrome'], viewport: { width: 1440, height: 900 } })

test.beforeEach(async ({ page }) => {
  await page.goto('/')
  await page.evaluate(() => localStorage.clear())
  await page.goto('/')
  await page.getByLabel('Password').fill('fleetpocket')
  await page.getByLabel('Authenticator code').fill('123456')
  await expect(page.getByRole('heading', { name: 'Tasks' })).toBeVisible()
})

test('run a task on a computer and follow it on the canvas', async ({ page }) => {
  // Mock mode has one computer, "MacBook Pro", with keys for every agent.
  await page.getByRole('link', { name: 'New' }).click()
  await page.getByLabel('Runs on').click()
  await page.getByRole('option', { name: 'MacBook Pro' }).click()
  await page.getByLabel('Agent').click()
  await page.getByRole('option', { name: 'Cursor' }).click()
  await page.getByLabel('What should Cursor do?').fill('Cache thumbnails on disk')
  await page.getByRole('button', { name: 'Start task' }).click()
  await expect(page.getByRole('heading', { name: 'Cache thumbnails on disk' })).toBeVisible()
  await page.getByRole('tab', { name: 'Info' }).click()
  await expect(page.getByText('Runs on')).toBeVisible()

  await page.getByRole('link', { name: 'Canvas' }).click()
  const mac = page.getByRole('region', { name: 'Cache thumbnails on disk' })
  await expect(mac).toBeVisible()
  await expect(page.getByText('MacBook Pro', { exact: true })).toBeVisible()

  // Answer a question from its terminal.
  const waiting = page.getByRole('region', { name: 'Retry webhook deliveries with backoff' })
  await waiting.getByRole('button', { name: '5 attempts' }).click()
  await expect(waiting.getByText('❯ 5 attempts')).toBeVisible()

  // Minimise, hide and bring back a window.
  await mac.getByRole('button', { name: 'Minimize' }).click()
  await expect(mac.getByRole('textbox')).toHaveCount(0)
  await mac.getByRole('button', { name: 'Hide from canvas' }).click()
  await expect(mac).toHaveCount(0)
  await page.getByRole('button', { name: '1 hidden' }).click()
  await expect(mac).toBeVisible()
})

test('add this computer with one click while its setup waits', async ({ page }) => {
  // fleetpocket-node setup answers on the loopback port with its pairing code
  // (mock mode serves it when this flag is set).
  await page.evaluate(() => localStorage.setItem('fleetpocket.mock.localSetup', '1'))
  await page.getByRole('link', { name: 'Settings' }).click()
  await page.getByRole('button', { name: 'Add computer' }).click()
  await expect(page.getByText('curl -fsSL')).toContainText('/api/v1/node/install | sh')
  await expect(page.getByText('This computer')).toBeVisible()
  await expect(page.getByLabel('Pairing code')).toHaveValue('KQ7-4MX')
  await expect(page.getByLabel('Name')).toHaveValue('Studio Mac')
  await page.getByRole('button', { name: 'Connect' }).click()
  await expect(page.getByText('Studio Mac is paired')).toBeVisible()
  await expect(page.getByText('Studio Mac is online')).toBeVisible()
  await page.getByRole('button', { name: 'Done' }).click()
  await expect(page.getByText('studio-mac', { exact: true })).toBeVisible()
})

test('a code from another device needs care', async ({ page }) => {
  await page.goto('/settings?pair=kq74mx')
  await expect(page.getByLabel('Pairing code')).toHaveValue('KQ7-4MX')
  await expect(page.getByText(/Connect it only if you ran setup on that computer yourself/)).toBeVisible()
  await expect(page.getByText('Request from 203.0.113.7')).toBeVisible()
  await page.getByLabel('Pairing code').fill('AAA-AAA')
  await expect(page.getByText(/No computer is waiting with this code/)).toBeVisible()
})

test('register a computer and see its token once', async ({ page }) => {
  await page.getByRole('link', { name: 'Settings' }).click()
  await page.getByRole('button', { name: 'Add computer' }).click()
  await page.getByRole('button', { name: 'Set up by hand with a token' }).click()
  await page.getByLabel('Name').fill('Studio Mac')
  await expect(page.getByLabel('Id')).toHaveValue('studio-mac')
  await page.getByRole('button', { name: 'Create and show token' }).click()
  await expect(page.getByRole('heading', { name: 'Connect this computer' })).toBeVisible()
  await expect(page.getByText(/^fpn_studio-mac\./)).toBeVisible()
  await page.getByRole('button', { name: 'Done' }).click()
  await expect(page.getByText('Never connected')).toBeVisible()
})
