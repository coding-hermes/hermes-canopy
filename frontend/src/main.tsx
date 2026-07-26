import { StrictMode } from 'react'
import { createRoot } from 'react-dom/client'
import './index.css'
import Root from './Root'
import { registerSW } from './serviceWorkerRegistration'

// Register service worker for offline support (best-effort)
void registerSW().then((status) => {
  if (status.supported && status.registered) {
    console.log('[SW] Registered — offline mode available')
  }
})

createRoot(document.getElementById('root')!).render(
  <StrictMode>
    <Root />
  </StrictMode>,
)
