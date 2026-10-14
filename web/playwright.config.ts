import { defineConfig, devices } from '@playwright/test'

const PORT = 5175

// Smoke tests run against mock mode (MSW + fake WebSocket), no backend needed.
export default defineConfig({
  testDir: 'e2e',
  timeout: 30_000,
  // One worker: Vite's first-run dependency optimisation races parallel page loads.
  workers: 1,
  use: { baseURL: `http://localhost:${PORT}`, trace: 'retain-on-failure' },
  projects: [{ name: 'phone', use: { ...devices['Pixel 7'], browserName: 'chromium' } }],
  webServer: {
    command: `VITE_MOCK=1 npx vite --port ${PORT} --strictPort`,
    port: PORT,
    reuseExistingServer: !process.env.CI,
  },
})
