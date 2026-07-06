import './i18n'
import React, { Suspense } from 'react'
import ReactDOM from 'react-dom/client'
import { BrowserRouter } from 'react-router-dom'
import App from './App'
import { setSession } from './lib/api'
import '@shared/styles.css'

// HTTPS enforcement — allow HTTP only on localhost
if (location.protocol === 'http:' && location.hostname !== 'localhost' && location.hostname !== '127.0.0.1') {
  document.body.innerText = 'HTTPS is required for Mist Drive. Please use https://'
  throw new Error('HTTPS required')
}

// Single-sign-on handoff from the desktop app. When the user clicks
// "Web ↗" in the desktop navbar, Wails opens the browser with
// `#token=<jwt>` appended. We consume it here, store the session
// through the SAME api-layer setter the login page uses (localStorage —
// a raw sessionStorage write here once broke the whole handoff when the
// session layer migrated for "remember me"), then scrub the fragment so
// the token never ends up in history or a share. If the token is
// invalid the app simply lands on /login as usual.
if (location.hash.startsWith('#token=')) {
  const tok = decodeURIComponent(location.hash.slice('#token='.length))
  history.replaceState(null, '', location.pathname + location.search)
  if (tok) {
    fetch('/api/me', { headers: { Authorization: 'Bearer ' + tok } })
      .then((r) => (r.ok ? r.json() : Promise.reject()))
      .then((u) => setSession(tok, u))
      .catch(() => {}) // invalid/expired token: land on /login as before
      .finally(bootReact)
  } else {
    bootReact()
  }
} else {
  bootReact()
}

function bootReact() {
  ReactDOM.createRoot(document.getElementById('root')!).render(
    <React.StrictMode>
      <Suspense fallback={null}>
        <BrowserRouter>
          <App />
        </BrowserRouter>
      </Suspense>
    </React.StrictMode>,
  )
}

