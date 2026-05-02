import { useState } from 'react'
import { useNavigate } from 'react-router-dom'
import { api, setSession, isRemembered, getSavedLogin } from '../lib/api'
import LoginCard from '@shared/components/LoginCard'

type Props = { version?: string }

export default function Login({ version }: Props) {
  const nav = useNavigate()
  const [login, setLogin] = useState(getSavedLogin)
  const [password, setPassword] = useState('')
  const [remember, setRemember] = useState(isRemembered)
  const [err, setErr] = useState<string | null>(null)
  const [busy, setBusy] = useState(false)

  const submit = async (e: React.FormEvent) => {
    e.preventDefault()
    setErr(null); setBusy(true)
    try {
      const res = await api.login(login, password)
      setSession(res.token, res.user, remember)
      nav('/files')
    } catch (e: any) {
      setErr(e.message || 'login failed')
    } finally {
      setBusy(false)
    }
  }

  return (
    <LoginCard
      version={version}
      login={login}
      onLoginChange={setLogin}
      password={password}
      onPasswordChange={setPassword}
      remember={remember}
      onRememberChange={setRemember}
      err={err}
      busy={busy}
      submitDisabled={!login || !password}
      onSubmit={submit}
    />
  )
}
