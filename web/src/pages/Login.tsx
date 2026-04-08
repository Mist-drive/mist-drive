import { useState } from 'react'
import { useNavigate } from 'react-router-dom'
import { api, setSession } from '../lib/api'

export default function Login() {
  const nav = useNavigate()
  const [login, setLogin] = useState('')
  const [password, setPassword] = useState('')
  const [err, setErr] = useState<string | null>(null)
  const [busy, setBusy] = useState(false)

  const submit = async (e: React.FormEvent) => {
    e.preventDefault()
    setErr(null); setBusy(true)
    try {
      const res = await api.login(login, password)
      setSession(res.token, res.user)
      nav('/files')
    } catch (e: any) {
      setErr(e.message || 'login failed')
    } finally {
      setBusy(false)
    }
  }

  return (
    <div className="login-wrap">
      <form className="login-card" onSubmit={submit}>
        <div className="logo" style={{ justifyContent: 'center', display: 'flex', marginBottom: '1rem' }}>
          <span className="logo-dot" />
          <span className="logo-text">MIST&nbsp;DRIVE</span>
        </div>
        <h2>Sign in</h2>
        <label>Login</label>
        <input value={login} onChange={e => setLogin(e.target.value)} autoFocus />
        <label>Password</label>
        <input type="password" value={password} onChange={e => setPassword(e.target.value)} />
        {err && <p className="error">{err}</p>}
        <button disabled={busy} style={{ marginTop: '1.4rem', width: '100%' }}>
          {busy ? 'Signing in...' : 'Login'}
        </button>
      </form>
    </div>
  )
}
