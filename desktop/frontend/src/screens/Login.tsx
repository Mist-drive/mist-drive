import { useEffect, useState } from 'react'
import { GetSettings } from '../../wailsjs/go/main/App'

type Props = {
  onLogin: (apiURL: string, login: string, password: string) => Promise<void>
}

// Simple centered login card. Pre-fills the API URL from settings so
// returning users only re-type their password on token expiry.
export default function Login({ onLogin }: Props) {
  const [apiURL, setApiURL] = useState('http://localhost:3000')
  const [login, setLogin] = useState('')
  const [password, setPassword] = useState('')
  const [err, setErr] = useState<string | null>(null)
  const [busy, setBusy] = useState(false)

  useEffect(() => {
    GetSettings().then((s) => {
      if (s.apiUrl) setApiURL(s.apiUrl)
      if (s.login) setLogin(s.login)
    })
  }, [])

  const submit = async (e: React.FormEvent) => {
    e.preventDefault()
    setErr(null)
    setBusy(true)
    try {
      await onLogin(apiURL, login, password)
    } catch (e: any) {
      setErr(String(e?.message ?? e))
    } finally {
      setBusy(false)
    }
  }

  return (
    <div className="login-wrap">
      <form className="login-card" onSubmit={submit}>
        <h2>Mist Drive</h2>
        <label>API URL</label>
        <input value={apiURL} onChange={(e) => setApiURL(e.target.value)} />
        <label>Login</label>
        <input value={login} onChange={(e) => setLogin(e.target.value)} autoFocus />
        <label>Password</label>
        <input
          type="password"
          value={password}
          onChange={(e) => setPassword(e.target.value)}
        />
        <button type="submit" disabled={busy || !login || !password}>
          {busy ? 'Signing in…' : 'Sign in'}
        </button>
        {err && <p className="error">{err}</p>}
      </form>
    </div>
  )
}
