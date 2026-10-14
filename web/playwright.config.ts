import { defineConfig, devices } from '@playwright/test'

// Two modes:
// - default: smoke tests against mock mode (MSW + fake WebSocket), no backend needed.
// - E2E_BACKEND=<url>: e2e/real against a running Anora Runner (deploy/docker-compose.dev.yml
//   in anora-runner, fake runner). Vite proxies /api to it. Needs E2E_PASSWORD and
//   E2E_TOTP_SECRET; the backend's CORS_ORIGINS must include the Vite origin (WebSocket).
const backend = process.env.E2E_BACKEND
const PORT = Number(process.env.E2E_PORT ?? (backend ? 5173 : 5175))

export default defineConfig({
  testDir: backend ? 'e2e/real' : 'e2e',
  testIgnore: backend ? undefined : 'real/**',
  timeout: backend ? 120_000 : 30_000,
  // One worker: Vite's first-run dependency optimisation races parallel page loads,
  // and the real backend runs tasks with a concurrency limit.
  workers: 1,
  use: { baseURL: `http://localhost:${PORT}`, trace: 'retain-on-failure' },
  projects: [{ name: 'phone', use: { ...devices['Pixel 7'], browserName: 'chromium' } }],
  webServer: {
    command: backend
      ? `ANORA_BACKEND_URL=${backend} VITE_MOCK=0 npx vite --port ${PORT} --strictPort`
      : `VITE_MOCK=1 npx vite --port ${PORT} --strictPort`,
    port: PORT,
    reuseExistingServer: !process.env.CI,
  },
})
