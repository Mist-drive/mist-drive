import { useEffect, useState } from 'react'
import { GetSettings, ListEnvironments } from '../../wailsjs/go/main/App'

type Props = {
  onLogin: (apiURL: string, login: string, password: string) => Promise<void>
}

// Simple centered login card. Pre-fills the API URL from settings so
// returning users only re-type their password on token expiry. A
// dropdown lists all previously used environments; picking "Custom…"
// lets the user type a new URL.
export default function Login({ onLogin }: Props) {
  const [apiURL, setApiURL] = useState('http://localhost:3000')
  const [envs, setEnvs] = useState<string[]>([])
  const [custom, setCustom] = useState(false)
  const [login, setLogin] = useState('')
  const [password, setPassword] = useState('')
  const [err, setErr] = useState<string | null>(null)
  const [busy, setBusy] = useState(false)

  useEffect(() => {
    GetSettings().then((s) => {
      if (s.apiUrl) setApiURL(s.apiUrl)
      if (s.login) setLogin(s.login)
    })
    ListEnvironments().then((list) => {
      if (list?.length) setEnvs(list)
    })
  }, [])

  const onSelectEnv = (value: string) => {
    if (value === '__custom__') {
      setCustom(true)
      setApiURL('')
    } else {
      setCustom(false)
      setApiURL(value)
    }
  }

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
        <label>Server</label>
        {envs.length > 0 && !custom ? (
          <select value={apiURL} onChange={(e) => onSelectEnv(e.target.value)}>
            {envs.map((url) => (
              <option key={url} value={url}>{url}</option>
            ))}
            <option value="__custom__">Custom…</option>
          </select>
        ) : (
          <div style={{ position: 'relative' }}>
            <input
              value={apiURL}
              onChange={(e) => setApiURL(e.target.value)}
              onKeyDown={(e) => { if (e.key === 'Escape' && envs.length > 0) { setCustom(false); setApiURL(envs[0]) } }}
              placeholder="https://drive.example.com"
              autoFocus
            />
            {envs.length > 0 && (
              <button type="button" className="back-link"
                onClick={() => { setCustom(false); setApiURL(envs[0]) }}>
                Back to list
              </button>
            )}
          </div>
        )}
        <label>Login</label>
        <input value={login} onChange={(e) => setLogin(e.target.value)} autoFocus />
        <label>Password</label>
        <input
          type="password"
          value={password}
          onChange={(e) => setPassword(e.target.value)}
        />
        <button type="submit" disabled={busy || !login || !password || !apiURL}>
          {busy ? 'Signing in…' : 'Sign in'}
        </button>
        {err && <p className="error">{err}</p>}
      </form>
    </div>
  )
}
