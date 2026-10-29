import { StrictMode } from 'react'
import { createRoot } from 'react-dom/client'
import { App } from './App'
import { resolveRuntime } from './telegram/runtime'
import './index.css'

async function enableMocks() {
  if (import.meta.env.VITE_MOCK !== '1') return
  const { startMocks } = await import('./mocks/browser')
  await startMocks()
}

void enableMocks().then(() => {
  createRoot(document.getElementById('root')!).render(
    <StrictMode>
      <App runtime={resolveRuntime()} />
    </StrictMode>,
  )
})

// Installable app (PWA): only in production builds; mock mode uses MSW's own worker.
if (import.meta.env.PROD && import.meta.env.VITE_MOCK !== '1' && 'serviceWorker' in navigator) {
  window.addEventListener('load', () => {
    navigator.serviceWorker.register('/sw.js').catch(() => {
      // Not installable then; the app works the same.
    })
  })
}
