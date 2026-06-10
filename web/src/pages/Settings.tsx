import { useEffect, useState } from 'react'
import QRCode from 'qrcode'
import { api, clearSession, type PublicDevice, type LoginRecord } from '../lib/api'
import { useConfirm } from '../components/ConfirmDialog'
import { useTranslation } from '@shared/lib/i18n'

type Phase =
  | 'idle'
  | 'enabling-qr'
  | 'enabling-confirm'
  | 'backup-shown'
  | 'disabling'
  | 'regen-confirm'
  | 'regen-shown'

export default function Settings() {
  const { t } = useTranslation()
  const confirm = useConfirm()
  const [totpEnabled, setTotpEnabled] = useState(false)
  const [phase, setPhase] = useState<Phase>('idle')
  const [qrDataUrl, setQrDataUrl] = useState('')
  const [secret, setSecret] = useState('')
  const [confirmCode, setConfirmCode] = useState('')
  const [enablePassword, setEnablePassword] = useState('')
  const [disablePassword, setDisablePassword] = useState('')
  const [disableCode, setDisableCode] = useState('')
  const [regenCode, setRegenCode] = useState('')
  const [backupCodes, setBackupCodes] = useState<string[]>([])
  const [err, setErr] = useState<string | null>(null)
  const [busy, setBusy] = useState(false)
  const [devices, setDevices] = useState<PublicDevice[]>([])
  const [loginHistory, setLoginHistory] = useState<LoginRecord[]>([])
  const [email, setEmail] = useState('')
  const [emailSaving, setEmailSaving] = useState(false)
  const [emailMsg, setEmailMsg] = useState<string | null>(null)
  const [currentPwd, setCurrentPwd] = useState('')
  const [newPwd, setNewPwd] = useState('')
  const [changePwdTotp, setChangePwdTotp] = useState('')
  const [changePwdMsg, setChangePwdMsg] = useState<string | null>(null)
  const [changePwdErr, setChangePwdErr] = useState<string | null>(null)
  const [changePwdBusy, setChangePwdBusy] = useState(false)
  const [logoutAllTotp, setLogoutAllTotp] = useState('')
  const [logoutAllErr, setLogoutAllErr] = useState<string | null>(null)
  const [logoutAllBusy, setLogoutAllBusy] = useState(false)
  const [logoutAllTotpVisible, setLogoutAllTotpVisible] = useState(false)

  useEffect(() => {
    api.me().then(u => { setTotpEnabled(u.totpEnabled); setEmail(u.email ?? '') }).catch(() => {})
    api.devices.list().then(setDevices).catch(() => {})
    api.loginHistory().then(setLoginHistory).catch(() => {})
  }, [])

  const startEnable = async () => {
    setErr(null); setBusy(true)
    try {
      const { secret: s, uri } = await api.totp.setup()
      setSecret(s)
      const dataUrl = await QRCode.toDataURL(uri, { width: 220, margin: 1 })
      setQrDataUrl(dataUrl)
      setConfirmCode('')
      setEnablePassword('')
      setPhase('enabling-qr')
    } catch (e: any) { setErr(e.message) }
    finally { setBusy(false) }
  }

  const confirmEnable = async (ev: React.FormEvent) => {
    ev.preventDefault()
    setErr(null); setBusy(true)
    try {
      const { backupCodes: codes } = await api.totp.enable(secret, confirmCode, enablePassword)
      setBackupCodes(codes)
      setTotpEnabled(true)
      setEnablePassword('')
      setPhase('backup-shown')
    } catch (e: any) { setErr(e.message) }
    finally { setBusy(false) }
  }

  const confirmDisable = async (ev: React.FormEvent) => {
    ev.preventDefault()
    setErr(null); setBusy(true)
    try {
      await api.totp.disable(disablePassword, disableCode)
      setTotpEnabled(false)
      setDevices([])
      setDisablePassword(''); setDisableCode('')
      setPhase('idle')
    } catch (e: any) { setErr(e.message) }
    finally { setBusy(false) }
  }

  const confirmRegen = async (ev: React.FormEvent) => {
    ev.preventDefault()
    setErr(null); setBusy(true)
    try {
      const { backupCodes: codes } = await api.totp.regenBackup(regenCode)
      setBackupCodes(codes)
      setRegenCode('')
      setPhase('regen-shown')
    } catch (e: any) { setErr(e.message) }
    finally { setBusy(false) }
  }

  const cancel = () => { setPhase('idle'); setErr(null) }

  const saveEmail = async (ev: React.FormEvent) => {
    ev.preventDefault()
    setEmailMsg(null); setEmailSaving(true)
    try {
      await api.updateEmail(email)
      setEmailMsg(t('settings.emailSaved'))
    } catch (e: any) { setEmailMsg(e.message) }
    finally { setEmailSaving(false) }
  }

  const submitChangePassword = async (ev: React.FormEvent) => {
    ev.preventDefault()
    setChangePwdErr(null); setChangePwdMsg(null); setChangePwdBusy(true)
    try {
      await api.changePassword(currentPwd, newPwd, totpEnabled ? changePwdTotp || undefined : undefined)
      setChangePwdMsg(t('settings.passwordChanged'))
      setCurrentPwd(''); setNewPwd(''); setChangePwdTotp('')
    } catch (e: any) { setChangePwdErr(e.message) }
    finally { setChangePwdBusy(false) }
  }

  const doLogoutAll = async () => {
    setLogoutAllErr(null); setLogoutAllBusy(true)
    try {
      await api.logoutAll(totpEnabled ? { totpCode: logoutAllTotp } : {})
      clearSession()
      window.location.replace('/login')
    } catch (e: any) { setLogoutAllErr(e.message); setLogoutAllBusy(false) }
  }

  const handleLogoutAll = async () => {
    if (totpEnabled) {
      setLogoutAllTotp(''); setLogoutAllErr(null)
      setLogoutAllTotpVisible(true)
    } else {
      if (!await confirm({ title: t('settings.logoutAllConfirmTitle'), message: t('settings.logoutAllConfirmMessage'), danger: true })) return
      doLogoutAll()
    }
  }

  const revokeDevice = async (id: string) => {
    if (!await confirm({ title: t('settings.revokeDeviceConfirmTitle'), message: t('settings.revokeDeviceConfirmMessage'), danger: true })) return
    await api.devices.revoke(id)
    setDevices(ds => ds.filter(d => d.id !== id))
  }

  const revokeAll = async () => {
    if (!await confirm({ title: t('settings.revokeAllConfirmTitle'), message: t('settings.revokeAllConfirmMessage'), danger: true })) return
    await api.devices.revokeAll()
    setDevices([])
  }

  return (
    <div className="settings-page">
      <div className="settings-columns">
      <div className="settings-col-left">
      <section className="settings-section">
        <h3>{t('settings.security')}</h3>
        <div className="settings-row">
          <span>{t('settings.totp')}</span>
          <span className={`badge ${totpEnabled ? 'badge-ok' : 'badge-off'}`}>
            {totpEnabled ? t('settings.totpEnabled') : t('settings.totpDisabled')}
          </span>
        </div>

        {phase === 'idle' && (
          <div className="settings-actions">
            {!totpEnabled ? (
              <button onClick={startEnable} disabled={busy}>{t('settings.enableTotp')}</button>
            ) : (
              <>
                <button onClick={() => { setPhase('disabling'); setErr(null) }} className="danger">
                  {t('settings.disableTotp')}
                </button>
                <button onClick={() => { setPhase('regen-confirm'); setErr(null) }}>
                  {t('settings.regenBackup')}
                </button>
              </>
            )}
          </div>
        )}

        {phase === 'enabling-qr' && (
          <form className="totp-form" onSubmit={confirmEnable}>
            <p>{t('settings.scanQr')}</p>
            {qrDataUrl && <img src={qrDataUrl} alt="TOTP QR code" width={220} height={220} />}
            <p className="muted">{t('settings.manualSecret')} <code>{secret}</code></p>
            <p>{t('settings.enterCodeToConfirm')}</p>
            <input
              type="text"
              inputMode="numeric"
              autoComplete="one-time-code"
              maxLength={6}
              value={confirmCode}
              onChange={e => setConfirmCode(e.target.value)}
              autoFocus
            />
            <input
              type="password"
              autoComplete="current-password"
              placeholder={t('login.password')}
              value={enablePassword}
              onChange={e => setEnablePassword(e.target.value)}
            />
            {err && <p className="error">{err}</p>}
            <div className="form-row">
              <button type="submit" disabled={busy || confirmCode.length < 6 || !enablePassword}>{t('settings.confirm')}</button>
              <button type="button" className="ghost" onClick={cancel}>{t('settings.cancel')}</button>
            </div>
          </form>
        )}

        {phase === 'backup-shown' && (
          <div className="totp-form">
            <h4>{t('settings.backupCodesTitle')}</h4>
            <p className="muted">{t('settings.backupCodesHint')}</p>
            <ul className="backup-codes">
              {backupCodes.map(c => <li key={c}><code>{c}</code></li>)}
            </ul>
            <button onClick={cancel}>{t('settings.done')}</button>
          </div>
        )}

        {phase === 'disabling' && (
          <form className="totp-form" onSubmit={confirmDisable}>
            <p>{t('settings.enterTotpToDisable')}</p>
            <input
              type="password"
              placeholder={t('login.password')}
              value={disablePassword}
              onChange={e => setDisablePassword(e.target.value)}
              autoFocus
            />
            <input
              type="text"
              inputMode="numeric"
              autoComplete="one-time-code"
              placeholder={t('login.totpCodePlaceholder')}
              maxLength={10}
              value={disableCode}
              onChange={e => setDisableCode(e.target.value)}
            />
            {err && <p className="error">{err}</p>}
            <div className="form-row">
              <button type="submit" className="danger" disabled={busy || !disablePassword || !disableCode}>
                {t('settings.disableTotp')}
              </button>
              <button type="button" className="ghost" onClick={cancel}>{t('settings.cancel')}</button>
            </div>
          </form>
        )}

        {phase === 'regen-confirm' && (
          <form className="totp-form" onSubmit={confirmRegen}>
            <p>{t('settings.enterTotpToRegen')}</p>
            <input
              type="text"
              inputMode="numeric"
              autoComplete="one-time-code"
              maxLength={6}
              value={regenCode}
              onChange={e => setRegenCode(e.target.value)}
              autoFocus
            />
            {err && <p className="error">{err}</p>}
            <div className="form-row">
              <button type="submit" disabled={busy || regenCode.length < 6}>{t('settings.regenBackup')}</button>
              <button type="button" className="ghost" onClick={cancel}>{t('settings.cancel')}</button>
            </div>
          </form>
        )}

        {phase === 'regen-shown' && (
          <div className="totp-form">
            <h4>{t('settings.backupCodesTitle')}</h4>
            <p className="muted">{t('settings.backupCodesHint')}</p>
            <ul className="backup-codes">
              {backupCodes.map(c => <li key={c}><code>{c}</code></li>)}
            </ul>
            <button onClick={cancel}>{t('settings.done')}</button>
          </div>
        )}
      </section>

      <section className="settings-section">
          <h3>{t('settings.trustedDevices')}</h3>
          {devices.length === 0 ? (
            <p className="muted">{t('settings.noDevices')}</p>
          ) : (
            <>
              <ul className="device-list">
                {devices.map(d => (
                  <li key={d.id} className="device-item">
                    <div className="device-info">
                      <span className="device-label">{d.label || t('settings.unknownDevice')}</span>
                      <span className="muted device-expiry">{t('settings.deviceExpires', { date: new Date(d.expiresAt).toLocaleDateString() })}</span>
                    </div>
                    <button className="ghost" onClick={() => revokeDevice(d.id)}>{t('settings.revokeDevice')}</button>
                  </li>
                ))}
              </ul>
              <div className="settings-actions">
                <button className="danger ghost" onClick={revokeAll}>{t('settings.revokeAll')}</button>
              </div>
            </>
          )}
        </section>

      <section className="settings-section">
        <h3>{t('settings.notificationEmail')}</h3>
        <form className="totp-form" onSubmit={saveEmail}>
          <input
            type="email"
            placeholder={t('settings.emailPlaceholder')}
            value={email}
            onChange={e => setEmail(e.target.value)}
          />
          {emailMsg && <p className={emailMsg === t('settings.emailSaved') ? 'muted' : 'error'}>{emailMsg}</p>}
          <div className="form-row">
            <button type="submit" disabled={emailSaving}>{t('settings.confirm')}</button>
          </div>
        </form>
      </section>

      <section className="settings-section">
        <h3>{t('settings.changePassword')}</h3>
        <form className="totp-form" onSubmit={submitChangePassword}>
          <input
            type="password"
            placeholder={t('settings.currentPassword')}
            value={currentPwd}
            onChange={e => setCurrentPwd(e.target.value)}
          />
          <input
            type="password"
            placeholder={t('settings.newPassword')}
            value={newPwd}
            onChange={e => setNewPwd(e.target.value)}
          />
          {totpEnabled && (
            <input
              type="text"
              inputMode="numeric"
              placeholder={t('login.totpCodePlaceholder')}
              maxLength={10}
              value={changePwdTotp}
              onChange={e => setChangePwdTotp(e.target.value)}
            />
          )}
          {changePwdErr && <p className="error">{changePwdErr}</p>}
          {changePwdMsg && <p className="muted">{changePwdMsg}</p>}
          <div className="form-row">
            <button type="submit" disabled={changePwdBusy || !currentPwd || !newPwd}>
              {t('settings.changePassword')}
            </button>
          </div>
        </form>
      </section>

      </div>

      <div className="settings-col-right">
        <section className="settings-section">
          <h3>{t('settings.loginHistory')}</h3>
          {loginHistory.length === 0 ? (
            <p className="muted">{t('settings.noLoginHistory')}</p>
          ) : (
            <ul className="login-history-list">
              {loginHistory.map((r, i) => (
                <li key={i} className="login-history-item">
                  <div className="login-history-info">
                    <span className="login-history-ip">{r.ip}</span>
                    {r.userAgent && <span className="muted login-history-ua">{r.userAgent}</span>}
                  </div>
                  <span className="muted login-history-date">
                    {new Date(r.at).toLocaleString()}
                  </span>
                </li>
              ))}
            </ul>
          )}
        </section>
        <section className="settings-section">
          <h3>{t('settings.logoutAll')}</h3>
          {logoutAllTotpVisible ? (
            <div className="totp-form">
              <input
                type="text"
                inputMode="numeric"
                placeholder={t('login.totpCodePlaceholder')}
                maxLength={10}
                value={logoutAllTotp}
                onChange={e => setLogoutAllTotp(e.target.value)}
                autoFocus
              />
              {logoutAllErr && <p className="error">{logoutAllErr}</p>}
              <div className="form-row">
                <button
                  className="danger"
                  disabled={logoutAllBusy || !logoutAllTotp}
                  onClick={doLogoutAll}
                >
                  {t('settings.logoutAll')}
                </button>
                <button type="button" className="ghost" onClick={() => setLogoutAllTotpVisible(false)}>
                  {t('settings.cancel')}
                </button>
              </div>
            </div>
          ) : (
            <div className="settings-actions">
              {logoutAllErr && <p className="error">{logoutAllErr}</p>}
              <button className="danger ghost" onClick={handleLogoutAll} disabled={logoutAllBusy}>
                {t('settings.logoutAll')}
              </button>
            </div>
          )}
        </section>
      </div>
      </div>
    </div>
  )
}
