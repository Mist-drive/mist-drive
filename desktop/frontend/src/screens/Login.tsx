import { useState, useEffect } from 'react'
import { GetSettings, GetVersion, ListEnvironments, Login, OpenGitHub } from '../../wailsjs/go/main/App'
import { apiclient } from '../../wailsjs/go/models'
import LoginCard from '@shared/components/LoginCard'
import { useTranslation } from '@shared/lib/i18n'
import { startLoading, endLoading } from '@shared/lib/loading'

type Props = {
  onLogin: (user: apiclient.PublicUser) => void
}

export default function LoginScreen({ onLogin }: Props) {
  const { t } = useTranslation()
  const [apiURL, setApiURL] = useState('http://localhost:3000')
  const [envs, setEnvs] = useState<string[]>([])
  const [custom, setCustom] = useState(false)
  const [login, setLogin] = useState('')
  const [password, setPassword] = useState('')
  const [rememberLogin, setRememberLogin] = useState(false)
  const [totpRequired, setTotpRequired] = useState(false)
  const [totpCode, setTotpCode] = useState('')
  const [rememberDevice, setRememberDevice] = useState(false)
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
    startLoading()
    try {
      const result = await Login(
        apiURL, login, password,
        totpRequired ? totpCode : '',
        rememberLogin,
        totpRequired ? rememberDevice : false,
      )
      if (result.totp_required) {
        setTotpRequired(true)
        setTotpCode('')
        return
      }
      onLogin(result.user)
    } catch (e: any) {
      setErr(String(e?.message ?? e))
    } finally {
      setBusy(false)
      endLoading()
    }
  }

  const serverSlot = envs.length > 0 && !custom ? (
    <select value={apiURL} onChange={(e) => onSelectEnv(e.target.value)}>
      {envs.map((url) => (
        <option key={url} value={url}>{url}</option>
      ))}
      <option value="__custom__">{t('desktop.customServer')}</option>
    </select>
  ) : (
    <div style={{ position: 'relative' }}>
      <input
        value={apiURL}
        onChange={(e) => setApiURL(e.target.value)}
        onKeyDown={(e) => { if (e.key === 'Escape' && envs.length > 0) { setCustom(false); setApiURL(envs[0]) } }}
        placeholder={t('desktop.serverPlaceholder')}
        autoFocus={!totpRequired}
      />
      {envs.length > 0 && (
        <button type="button" className="back-link"
          onClick={() => { setCustom(false); setApiURL(envs[0]) }}>
          {t('desktop.backToList')}
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
      submitDisabled={!login || !password || (totpRequired && !totpCode) || !apiURL}
      onSubmit={submit}
      submitLabel={busy && totpRequired ? t('login.verifying') : undefined}
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
          <label style={{ display: 'flex', flexDirection: 'row', alignItems: 'center', gap: '.5rem', cursor: 'pointer', marginTop: '.4rem' }}>
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
      footerSlot={
        <button type="button" className="back-link" style={{ display: 'block', margin: '1rem auto 0', textAlign: 'center' }}
          onClick={() => OpenGitHub()}>
          {t('desktop.visitWebsite')}
        </button>
      }
    />
  )
}
