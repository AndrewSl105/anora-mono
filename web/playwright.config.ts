import { defineConfig, devices } from '@playwright/test'

// Two modes:
// - default: smoke tests against mock mode (MSW + fake WebSocket), no backend needed.
// - E2E_BACKEND=<url>: e2e/real against a running Fleetpocket (deploy/docker-compose.dev.yml
//   at the repository root, fake agent). Vite proxies /api to it. Needs E2E_PASSWORD and
//   E2E_TOTP_SECRET; the backend's CORS_ORIGINS must include the Vite origin (WebSocket).
// - E2E_BASE_URL=<url>: e2e/real against a deployed instance (client and API on one
//   origin, e.g. https://fleetpocket.example.com); no dev server is started.
const deployed = process.env.E2E_BASE_URL
const backend = process.env.E2E_BACKEND ?? deployed
const PORT = Number(process.env.E2E_PORT ?? (backend ? 5173 : 5175))

export default defineConfig({
  testDir: backend ? 'e2e/real' : 'e2e',
  testIgnore: backend ? undefined : 'real/**',
  timeout: backend ? 120_000 : 30_000,
  // One worker: Vite's first-run dependency optimisation races parallel page loads,
  // and the real backend runs tasks with a concurrency limit.
  workers: 1,
  use: { baseURL: deployed ?? `http://localhost:${PORT}`, ignoreHTTPSErrors: !!process.env.E2E_INSECURE_TLS, trace: 'retain-on-failure' },
  projects: [{ name: 'phone', use: { ...devices['Pixel 7'], browserName: 'chromium' } }],
  webServer: deployed
    ? undefined
    : {
        command: backend
          ? `FLEETPOCKET_BACKEND_URL=${backend} VITE_MOCK=0 npx vite --port ${PORT} --strictPort`
          : `VITE_MOCK=1 npx vite --port ${PORT} --strictPort`,
        port: PORT,
        reuseExistingServer: !process.env.CI,
      },
})
