// Fleetpocket service worker: makes the web app installable (PWA) and opens
// it offline with the last app shell. It never touches the API: /api, the
// WebSocket and webhooks always go to the network.
const CACHE = 'fleetpocket-shell-v1'

self.addEventListener('install', (event) => {
  event.waitUntil(caches.open(CACHE).then((c) => c.add('/')).then(() => self.skipWaiting()))
})

self.addEventListener('activate', (event) => {
  event.waitUntil(
    caches
      .keys()
      .then((keys) => Promise.all(keys.filter((k) => k !== CACHE).map((k) => caches.delete(k))))
      .then(() => self.clients.claim()),
  )
})

self.addEventListener('fetch', (event) => {
  const req = event.request
  if (req.method !== 'GET') return
  const url = new URL(req.url)
  if (url.origin !== self.location.origin) return
  if (url.pathname.startsWith('/api/') || url.pathname.startsWith('/webhooks/') || url.pathname === '/healthz') return

  // Pages: network first (deploys show up at once), the cached shell offline.
  if (req.mode === 'navigate') {
    event.respondWith(
      fetch(req)
        .then((res) => {
          if (res.ok) {
            const copy = res.clone()
            void caches.open(CACHE).then((c) => c.put('/', copy))
          }
          return res
        })
        .catch(() => caches.match('/').then((r) => r || Response.error())),
    )
    return
  }

  // Hashed build assets never change: cache first.
  if (url.pathname.startsWith('/assets/')) {
    event.respondWith(
      caches.match(req).then(
        (hit) =>
          hit ||
          fetch(req).then((res) => {
            if (res.ok) {
              const copy = res.clone()
              void caches.open(CACHE).then((c) => c.put(req, copy))
            }
            return res
          }),
      ),
    )
  }
})
