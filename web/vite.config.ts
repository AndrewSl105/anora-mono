/// <reference types="vitest/config" />
import { fileURLToPath, URL } from 'node:url'
import { defineConfig, loadEnv } from 'vite'
import react from '@vitejs/plugin-react'
import tailwindcss from '@tailwindcss/vite'
import { msw } from 'msw/vite'

export default defineConfig(({ mode }) => {
  const env = loadEnv(mode, process.cwd(), '')
  const backend = env.FLEETPOCKET_BACKEND_URL || 'http://localhost:8080'
  return {
    // msw only serves its worker script in dev; mock mode is never part of a production build.
    plugins: [react(), tailwindcss(), ...(mode === 'production' ? [] : [msw({ mode: 'worker-only' })])],
    resolve: { alias: { '@': fileURLToPath(new URL('./src', import.meta.url)) } },
    server: {
      // The WebSocket entry must come before /api so the upgrade is proxied.
      proxy: {
        '/api/v1/ws': { target: backend, ws: true, changeOrigin: true },
        '/api': { target: backend, changeOrigin: true },
        '/healthz': { target: backend, changeOrigin: true },
      },
    },
    build: { target: 'es2022', sourcemap: false, manifest: true },
    test: {
      environment: 'jsdom',
      setupFiles: ['./src/test/setup.ts'],
      include: ['src/**/*.test.{ts,tsx}'],
      css: false,
    },
  }
})
