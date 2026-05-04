import { type ReactNode } from 'react'
import Logo from './Logo'
import { useTranslation } from '@shared/lib/i18n'

type Props = {
  version?: string
  serverSlot?: ReactNode
  login: string
  onLoginChange: (v: string) => void
  password: string
  onPasswordChange: (v: string) => void
  remember: boolean
  onRememberChange: (v: boolean) => void
  err?: string | null
  busy?: boolean
  submitDisabled?: boolean
  onSubmit: (e: React.FormEvent) => void
}

export default function LoginCard({
  version,
  serverSlot,
  login,
  onLoginChange,
  password,
  onPasswordChange,
  remember,
  onRememberChange,
  err,
  busy,
  submitDisabled,
  onSubmit,
}: Props) {
  const { t } = useTranslation()
  return (
    <div className="login-wrap">
      <form className="login-card" onSubmit={onSubmit}>
        <div style={{ display: 'flex', justifyContent: 'center', marginBottom: '1.2rem' }}>
          <Logo version={version} />
        </div>
        {serverSlot && (
          <>
            <label>{t('login.server')}</label>
            {serverSlot}
          </>
        )}
        <label>{t('login.login')}</label>
        <input value={login} onChange={e => onLoginChange(e.target.value)} autoFocus={!serverSlot} />
        <label>{t('login.password')}</label>
        <input type="password" value={password} onChange={e => onPasswordChange(e.target.value)} />
        {err && <p className="error">{err}</p>}
        <label style={{ display: 'flex', flexDirection: 'row', alignItems: 'center', gap: '.5rem', cursor: 'pointer', marginTop: '.4rem' }}>
          <input
            type="checkbox"
            checked={remember}
            onChange={e => onRememberChange(e.target.checked)}
            style={{ width: 'auto', margin: 0 }}
          />
          {t('login.rememberMe')}
        </label>
        <button type="submit" disabled={busy || submitDisabled} style={{ marginTop: '1.4rem', width: '100%' }}>
          {busy ? t('login.signingIn') : t('login.signIn')}
        </button>
      </form>
    </div>
  )
}
