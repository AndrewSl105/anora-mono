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
