import { expect, test } from '@playwright/test'

test.beforeEach(async ({ page }) => {
  await page.goto('/')
  await page.evaluate(() => localStorage.clear())
  await page.goto('/')
})

async function login(page: import('@playwright/test').Page) {
  await page.getByLabel('Password').fill('anora')
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
