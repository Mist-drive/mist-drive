import { useEffect, useState } from 'react'
import { Login, Logout, Me } from '../wailsjs/go/main/App'
import { apiclient } from '../wailsjs/go/models'
import LoginScreen from './screens/Login'
import Home from './screens/Home'
import './App.css'

// Boot flow: we have a stored JWT in settings.json ⇒ try Me().
// If it succeeds, land on Home; otherwise show the Login screen.
// `null` = still checking, avoids a login-flash on startup.
export default function App() {
  const [user, setUser] = useState<apiclient.PublicUser | null>(null)
  const [checked, setChecked] = useState(false)

  useEffect(() => {
    Me()
      .then((u) => setUser(u))
      .catch(() => setUser(null))
      .finally(() => setChecked(true))
  }, [])

  if (!checked) return <div className="boot">Loading…</div>

  if (!user) {
    return (
      <LoginScreen
        onLogin={async (url, login, password) => {
          const u = await Login(url, login, password)
          setUser(u)
        }}
      />
    )
  }

  return (
    <Home
      user={user}
      onLogout={async () => {
        await Logout()
        setUser(null)
      }}
    />
  )
}
