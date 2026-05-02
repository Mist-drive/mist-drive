import { useEffect, useState } from 'react'
import { GetSettings, GetVersion, ListEnvironments } from '../../wailsjs/go/main/App'
import LoginCard from '@shared/components/LoginCard'

type Props = {
  onLogin: (apiURL: string, login: string, password: string, rememberLogin: boolean) => Promise<void>
}

export default function Login({ onLogin }: Props) {
  const [apiURL, setApiURL] = useState('http://localhost:3000')
  const [envs, setEnvs] = useState<string[]>([])
  const [custom, setCustom] = useState(false)
  const [login, setLogin] = useState('')
  const [password, setPassword] = useState('')
  const [rememberLogin, setRememberLogin] = useState(false)
  const [err, setErr] = useState<string | null>(null)
  const [busy, setBusy] = useState(false)
  const [version, setVersion] = useState('')

  useEffect(() => {
    GetSettings().then((s) => {
      if (s.apiUrl) setApiURL(s.apiUrl)
      if (s.login) setLogin(s.login)
      setRememberLogin(s.rememberLogin)
    })
    ListEnvironments().then((list) => {
      if (list?.length) setEnvs(list)
    })
    GetVersion().then(setVersion).catch(() => {})
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
      await onLogin(apiURL, login, password, rememberLogin)
    } catch (e: any) {
      setErr(String(e?.message ?? e))
    } finally {
      setBusy(false)
    }
  }

  const serverSlot = envs.length > 0 && !custom ? (
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
  )

  return (
    <LoginCard
      version={version || undefined}
      serverSlot={serverSlot}
      login={login}
      onLoginChange={setLogin}
      password={password}
      onPasswordChange={setPassword}
      remember={rememberLogin}
      onRememberChange={setRememberLogin}
      err={err}
      busy={busy}
      submitDisabled={!login || !password || !apiURL}
      onSubmit={submit}
    />
  )
}
