import { defineConfig } from '@playwright/test'

// README screenshots in mock mode (no backend): `npm run screenshots` or `make screenshots`.
// Kept out of playwright.config.ts so `npm run e2e` and CI never run it.
// Output: ../docs/screenshots/<screen>-<phone|desktop>-<light|dark>.png and the demo video
// (test-results/), which scripts/demo-gif.sh turns into ../docs/screenshots/demo.gif.
if (process.env.SCREENSHOTS !== '1') {
  throw new Error('Screenshots run only with SCREENSHOTS=1 (npm run screenshots).')
}

const PORT = 5176
const phone = { viewport: { width: 390, height: 844 }, deviceScaleFactor: 2, isMobile: true, hasTouch: true }
const desktop = { viewport: { width: 1440, height: 900 }, deviceScaleFactor: 1 }

export default defineConfig({
  testDir: 'e2e/screenshots',
  timeout: 60_000,
  workers: 1,
  use: {
    baseURL: `http://localhost:${PORT}`,
    browserName: 'chromium',
    locale: 'en-US',
    timezoneId: 'UTC',
  },
  projects: [
    { name: 'phone-light', use: { ...phone, colorScheme: 'light' } },
    { name: 'phone-dark', use: { ...phone, colorScheme: 'dark' } },
    { name: 'desktop-light', use: { ...desktop, colorScheme: 'light' } },
    { name: 'desktop-dark', use: { ...desktop, colorScheme: 'dark' } },
  ],
  webServer: {
    command: `VITE_MOCK=1 VITE_DEBUG= npx vite --port ${PORT} --strictPort`,
    port: PORT,
    reuseExistingServer: false,
  },
})
