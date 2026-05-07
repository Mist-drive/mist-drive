import { useEffect, useState } from 'react'
import { GetFeatures, Logout, Me } from '../wailsjs/go/main/App'
import { apiclient } from '../wailsjs/go/models'
import { ConfirmProvider } from './components/ConfirmDialog'
import LoadingBar from '@shared/components/LoadingBar'
import { startLoading, endLoading } from '@shared/lib/loading'
import { onSessionExpired } from './session'
import LoginScreen from './screens/Login'
import Home from './screens/Home'
import { useTranslation } from '@shared/lib/i18n'

// Boot flow: we have a stored JWT in settings.json ⇒ try Me().
// If it succeeds, land on Home; otherwise show the Login screen.
// `null` = still checking, avoids a login-flash on startup.
export default function App() {
  const [user, setUser] = useState<apiclient.PublicUser | null>(null)
  const [checked, setChecked] = useState(false)
  const [features, setFeatures] = useState<apiclient.Features>(new apiclient.Features())

  useEffect(() => onSessionExpired(() => setUser(null)), [])

  useEffect(() => {
    startLoading()
    Me()
      .then((u) => { setUser(u); GetFeatures().then(setFeatures).catch(() => {}) })
      .catch(() => setUser(null))
      .finally(() => { endLoading(); setChecked(true) })
  }, [])

  const { t } = useTranslation()
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
          onLogin={(u) => { setUser(u); GetFeatures().then(setFeatures).catch(() => {}) }}
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
