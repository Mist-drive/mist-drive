import { useState } from 'react'
import { useNavigate } from 'react-router-dom'
import { api, setSession, isRemembered, getSavedLogin } from '../lib/api'
import LoginCard from '@shared/components/LoginCard'
import { useTranslation } from '@shared/lib/i18n'

type Props = { version?: string }

export default function Login({ version }: Props) {
  const { t } = useTranslation()
  const nav = useNavigate()
  const [login, setLogin] = useState(getSavedLogin)
  const [password, setPassword] = useState('')
  const [remember, setRemember] = useState(isRemembered)
  const [totpRequired, setTotpRequired] = useState(false)
  const [totpCode, setTotpCode] = useState('')
  const [rememberDevice, setRememberDevice] = useState(false)
  const [err, setErr] = useState<string | null>(null)
  const [busy, setBusy] = useState(false)

  const submit = async (e: React.FormEvent) => {
    e.preventDefault()
    setErr(null); setBusy(true)
    try {
      const res = await api.login(login, password, totpRequired ? totpCode : undefined, totpRequired ? rememberDevice : undefined)
      if ('totp_required' in res) {
        setTotpRequired(true)
        setTotpCode('')
        return
      }
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
      submitDisabled={!login || !password || (totpRequired && !totpCode)}
      onSubmit={submit}
      totpSlot={totpRequired ? (
        <div className="form-group">
          <label>{t('login.totpCode')}</label>
          <input
            type="text"
            inputMode="numeric"
            autoComplete="one-time-code"
            placeholder={t('login.totpCodePlaceholder')}
            value={totpCode}
            onChange={e => setTotpCode(e.target.value)}
            autoFocus
            maxLength={10}
          />
          <label style={{ display: 'flex', flexDirection: 'row', alignItems: 'center', gap: '.5rem', cursor: 'pointer' }}>
            <input
              type="checkbox"
              checked={rememberDevice}
              onChange={e => setRememberDevice(e.target.checked)}
              style={{ width: 'auto', margin: 0 }}
            />
            {t('login.rememberTotpDevice')}
          </label>
        </div>
      ) : undefined}
      submitLabel={busy && totpRequired ? t('login.verifying') : undefined}
    />
  )
}
