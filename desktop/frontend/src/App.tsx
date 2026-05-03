import { useEffect, useState } from 'react'
import { Login, Logout, Me } from '../wailsjs/go/main/App'
import { apiclient } from '../wailsjs/go/models'
import { ConfirmProvider } from './components/ConfirmDialog'
import LoadingBar from '@shared/components/LoadingBar'
import { startLoading, endLoading } from '@shared/lib/loading'
import { onSessionExpired } from './session'
import LoginScreen from './screens/Login'
import Home from './screens/Home'

// Boot flow: we have a stored JWT in settings.json ⇒ try Me().
// If it succeeds, land on Home; otherwise show the Login screen.
// `null` = still checking, avoids a login-flash on startup.
export default function App() {
  const [user, setUser] = useState<apiclient.PublicUser | null>(null)
  const [checked, setChecked] = useState(false)

  useEffect(() => onSessionExpired(() => setUser(null)), [])

  useEffect(() => {
    startLoading()
    Me()
      .then((u) => setUser(u))
      .catch(() => setUser(null))
      .finally(() => { endLoading(); setChecked(true) })
  }, [])

  if (!checked) return <div className="boot">Loading…</div>

  return (
    <ConfirmProvider>
      <LoadingBar />
      <div className="background" aria-hidden>
        <div className="gradient gradient-1" />
        <div className="gradient gradient-2" />
        <div className="gradient gradient-3" />
      </div>
      {!user ? (
        <LoginScreen
          onLogin={async (url, login, password, rememberLogin) => {
            startLoading()
            try { const u = await Login(url, login, password, rememberLogin); setUser(u) }
            finally { endLoading() }
          }}
        />
      ) : (
        <Home
          user={user}
          onLogout={async () => {
            startLoading()
            try { await Logout(); setUser(null) }
            finally { endLoading() }
          }}
        />
      )}
    </ConfirmProvider>
  )
}
