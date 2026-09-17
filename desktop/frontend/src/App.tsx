import { useEffect, useState } from 'react'
import { GetFeatures, Logout, Me } from '../wailsjs/go/main/App'
import { apiclient } from '../wailsjs/go/models'
import { ConfirmProvider } from '@shared/components/ConfirmDialog'
import LoadingBar from '@shared/components/LoadingBar'
import { startLoading, endLoading } from '@shared/lib/loading'
import { onSessionExpired, onServerLost } from './session'
import LoginScreen from './screens/Login'
import Home from './screens/Home'
import { useTranslation } from '@shared/lib/i18n'

// Boot flow: we have a stored session (OS keyring) ⇒ try Me().
// If it succeeds, land on Home; otherwise show the Login screen.
// `null` = still checking, avoids a login-flash on startup.
export default function App() {
  const { t } = useTranslation()
  const [user, setUser] = useState<apiclient.PublicUser | null>(null)
  const [checked, setChecked] = useState(false)
  const [features, setFeatures] = useState<apiclient.Features>(new apiclient.Features())
  // One-shot friendly reason shown on the login screen when we got
  // kicked back there by a lost server connection (not a logout).
  const [notice, setNotice] = useState<string | null>(null)

  useEffect(() => onSessionExpired(() => {
    setUser(null)
    // Session expiry only resets frontend state by default — but the Go
    // backend's ws client keeps reconnecting with the now-stale token
    // (nothing else stops it) unless we also call the real Logout, which
    // clears settings and calls ws.Stop()/engine.Stop(). Without this the
    // app spins hammering /ws in the background while showing the login
    // screen. Fire-and-forget: the UI already reflects logged-out state.
    Logout().catch(() => {})
  }), [])

  // Server unreachable (API stopped, network gone): back to the login
  // screen with a human reason instead of screens stuck on raw errors.
  // Same Logout rationale as above — it also stops the ws/sync engines
  // from hammering a dead endpoint.
  useEffect(() => onServerLost(() => {
    setUser(null)
    setNotice(t('login.serverLost'))
    Logout().catch(() => {})
  }), [t])

  useEffect(() => {
    startLoading()
    Me()
      .then((u) => { setUser(u); GetFeatures().then(setFeatures).catch(() => {}) })
      .catch(() => setUser(null))
      .finally(() => { endLoading(); setChecked(true) })
  }, [])

  if (!checked) return <div className="boot">{t('desktop.loading')}</div>

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
          notice={notice}
          onLogin={(u) => { setNotice(null); setUser(u); GetFeatures().then(setFeatures).catch(() => {}) }}
        />
      ) : (
        <Home
          user={user}
          features={features}
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
