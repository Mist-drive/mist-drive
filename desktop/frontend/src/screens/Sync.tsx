import { useEffect, useState } from 'react'
import { useTranslation } from '@shared/lib/i18n'
import {
  AddSyncFolder,
  GetSettings,
  RemoveSyncFolder,
  SaveSettings,
  SetBandwidthLimits,
  SetFolderEnabled,
  SyncHistory,
  SyncStatus,
} from '../../wailsjs/go/main/App'
import { settings, sync } from '../../wailsjs/go/models'

// Sync control panel: lists folder mappings, lets the user add/remove
// them, starts/stops the engine, tweaks bandwidth. Status is polled
// every second while running (the engine could emit events via
// runtime.EventsEmit but a simple poll is one API call and doesn't
// require any subscription lifecycle).
export default function SyncPanel() {
  const { t } = useTranslation()
  const [s, setS] = useState<settings.Settings | null>(null)
  const [status, setStatus] = useState<sync.Status | null>(null)
  const [err, setErr] = useState<string | null>(null)
  const [historyOpen, setHistoryOpen] = useState(false)
  const [history, setHistory] = useState<sync.LogEntry[]>([])

  const refreshSettings = async () => setS(await GetSettings())
  const refreshStatus = async () => {
    try { setStatus(await SyncStatus()) } catch { /* ignore */ }
  }

  useEffect(() => { refreshSettings(); refreshStatus() }, [])

  useEffect(() => {
    const t = setInterval(refreshStatus, 1000)
    return () => clearInterval(t)
  }, [])

  const openHistory = async () => {
    setHistory(await SyncHistory())
    setHistoryOpen(true)
  }

  const onAdd = async () => {
    setErr(null)
    try {
      const added = await AddSyncFolder()
      if (added?.local) await refreshSettings()
    } catch (e: any) {
      setErr(String(e?.message ?? e))
    }
  }
  const onRemove = async (i: number) => {
    if (!confirm(t('sync.removeFolderConfirm'))) return
    await RemoveSyncFolder(i)
    await refreshSettings()
  }
  const onSaveLimits = async (mc: number, kbps: number) => {
    await SetBandwidthLimits(mc, kbps)
    await refreshSettings()
  }

  if (!s) return null

  return (
    <div className="card">
      <div className="row" style={{ justifyContent: 'space-between', marginBottom: '1rem' }}>
        <h3 style={{ margin: 0 }}>{t('sync.title')}</h3>
        <button className="ghost" onClick={openHistory} style={{ padding: '.3rem .7rem', fontSize: '0.8rem' }}>
          {t('sync.history')}
        </button>
      </div>

      {err && <p className="error" style={{ marginBottom: '.8rem' }}>{err}</p>}

      {status && (
        <p className="muted" style={{ marginBottom: '1rem', fontSize: '0.85rem' }}>
          ↑ {status.uploaded} ↓ {status.downloaded} = {status.skipped}
          {status.inFlight && <> · {status.inFlight}</>}
          {status.lastError && <> · <span style={{ color: 'var(--accent-red)' }}>{status.lastError}</span></>}
        </p>
      )}

      {historyOpen && <HistoryModal entries={history} onClose={() => setHistoryOpen(false)} />}

      <h4 style={{
        fontSize: '0.75rem',
        textTransform: 'uppercase',
        letterSpacing: 1,
        color: 'var(--text-secondary)',
        marginBottom: '.5rem',
      }}>{t('sync.folders')}</h4>
      {s.folders.length === 0 && (
        <p className="muted" style={{ fontSize: '0.85rem', marginBottom: '.8rem' }}>
          {t('sync.noFolders')}
        </p>
      )}
      {s.folders.map((f, i) => {
        const enabled = f.enabled
        const flipEnabled = async () => {
          await SetFolderEnabled(i, !enabled)
          await refreshSettings()
        }
        return (
          <div key={i} className="row" style={{
            justifyContent: 'space-between',
            padding: '.6rem .8rem',
            border: '1px solid var(--border)',
            borderRadius: 8,
            marginBottom: '.5rem',
          }}>
            <div style={{ flex: 1, minWidth: 0 }}>
              <div style={{ overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>
                {f.local}
              </div>
              <div className="muted" style={{ fontSize: '0.78rem' }}>
                ⇄ {f.remotePrefix || t('sync.bucketRoot')}
              </div>
            </div>
            <div className="row" style={{ gap: '.4rem' }}>
              <button
                className="ghost"
                onClick={flipEnabled}
                title={enabled ? t('sync.syncingTooltip') : t('sync.pausedTooltip')}
                style={{
                  padding: '.3rem .7rem',
                  fontSize: '0.8rem',
                  fontWeight: 600,
                  color: '#fff',
                  background: enabled ? 'var(--accent-green, #2ea043)' : 'var(--accent-red, #d14343)',
                  borderColor: enabled ? 'var(--accent-green, #2ea043)' : 'var(--accent-red, #d14343)',
                }}
              >{t('sync.title')}</button>
              <button className="danger" onClick={() => onRemove(i)}
                style={{ padding: '.3rem .6rem' }}>{t('sync.remove')}</button>
            </div>
          </div>
        )
      })}

      <div className="row" style={{ marginTop: '.8rem' }}>
        <button className="ghost" onClick={onAdd}>{t('sync.addFolder')}</button>
      </div>

      <h4 style={{
        fontSize: '0.75rem',
        textTransform: 'uppercase',
        letterSpacing: 1,
        color: 'var(--text-secondary)',
        marginTop: '1.5rem',
        marginBottom: '.5rem',
      }}>{t('sync.bandwidth')}</h4>
      <BandwidthForm
        concurrent={s.maxConcurrentUploads}
        kbps={s.maxUploadRateKBps}
        onSave={onSaveLimits}
      />

      <h4 style={{
        fontSize: '0.75rem',
        textTransform: 'uppercase',
        letterSpacing: 1,
        color: 'var(--text-secondary)',
        marginTop: '1.5rem',
        marginBottom: '.5rem',
      }}>{t('sync.behavior')}</h4>
      <label style={{
        display: 'flex',
        alignItems: 'center',
        gap: '.5rem',
        cursor: 'pointer',
        fontSize: '0.9rem',
        textTransform: 'none',
        letterSpacing: 'normal',
        color: 'var(--text-primary)',
        margin: 0,
      }}>
        <input
          type="checkbox"
          checked={s.closeToTray}
          onChange={async () => {
            s.closeToTray = !s.closeToTray
            await SaveSettings(s)
            await refreshSettings()
          }}
          style={{ width: 'auto', margin: 0 }}
        />
        {t('sync.closeToTray')}
      </label>
      <label style={{
        display: 'flex',
        alignItems: 'center',
        gap: '.5rem',
        cursor: 'pointer',
        fontSize: '0.9rem',
        textTransform: 'none',
        letterSpacing: 'normal',
        color: 'var(--text-primary)',
        margin: '.6rem 0 0',
      }}>
        <input
          type="checkbox"
          checked={s.notifications}
          onChange={async () => {
            s.notifications = !s.notifications
            await SaveSettings(s)
            await refreshSettings()
          }}
          style={{ width: 'auto', margin: 0 }}
        />
        {t('sync.notifications')}
      </label>
    </div>
  )
}

// Small controlled form so edits live locally until the user hits Save
// — saving on every keystroke would write to disk every character.
function BandwidthForm({
  concurrent,
  kbps,
  onSave,
}: {
  concurrent: number
  kbps: number
  onSave: (mc: number, kbps: number) => Promise<void>
}) {
  const { t } = useTranslation()
  const [mc, setMc] = useState(concurrent)
  const [kb, setKb] = useState(kbps)
  useEffect(() => { setMc(concurrent); setKb(kbps) }, [concurrent, kbps])

  return (
    <div className="row" style={{ gap: '.8rem', flexWrap: 'wrap' }}>
      <div style={{ flex: '1 1 180px' }}>
        <label>{t('sync.parallelUploads')}</label>
        <input
          type="number"
          min={1}
          value={mc}
          onChange={(e) => setMc(parseInt(e.target.value || '1', 10))}
        />
      </div>
      <div style={{ flex: '1 1 180px' }}>
        <label>{t('sync.maxUploadRate')}</label>
        <input
          type="number"
          min={0}
          value={kb}
          onChange={(e) => setKb(parseInt(e.target.value || '0', 10))}
        />
      </div>
      <button
        style={{ alignSelf: 'flex-end' }}
        onClick={() => onSave(mc, kb)}
      >{t('sync.save')}</button>
    </div>
  )
}

const actionIcon: Record<string, string> = {
  upload: '↑', download: '↓', delete: '✕', error: '!',
}

function HistoryModal({ entries, onClose }: { entries: sync.LogEntry[]; onClose: () => void }) {
  const { t } = useTranslation()
  return (
    <div
      onClick={onClose}
      style={{
        position: 'fixed', inset: 0, zIndex: 1000,
        background: 'rgba(0,0,0,0.6)', display: 'flex',
        alignItems: 'center', justifyContent: 'center',
      }}
    >
      <div
        onClick={(e) => e.stopPropagation()}
        style={{
          background: 'var(--bg-card)', border: '1px solid var(--border)',
          borderRadius: 16, padding: '1.5rem', width: '100%', maxWidth: 560,
          maxHeight: '70vh', display: 'flex', flexDirection: 'column',
        }}
      >
        <div className="row" style={{ justifyContent: 'space-between', marginBottom: '1rem' }}>
          <h3 style={{ margin: 0 }}>{t('sync.historyTitle')}</h3>
          <button className="ghost" onClick={onClose} style={{ padding: '.3rem .6rem' }}>{t('sync.close')}</button>
        </div>
        <div style={{ overflowY: 'auto', flex: 1, minHeight: 0 }}>
          {entries.length === 0 && (
            <p className="muted" style={{ fontSize: '0.85rem' }}>{t('sync.noActivity')}</p>
          )}
          {entries.map((e, i) => {
            const t = e.time ? new Date(e.time).toLocaleTimeString() : ''
            const isErr = e.action === 'error'
            return (
              <div key={i} style={{
                display: 'flex', gap: '.6rem', alignItems: 'baseline',
                padding: '.35rem 0',
                borderBottom: '1px solid var(--border)',
                fontSize: '0.85rem',
              }}>
                <span style={{
                  width: 16, textAlign: 'center', flexShrink: 0,
                  color: isErr ? 'var(--accent-red)' : 'var(--text-secondary)',
                }}>
                  {actionIcon[e.action] ?? '?'}
                </span>
                <span className="muted" style={{ flexShrink: 0, fontSize: '0.78rem' }}>{t}</span>
                <span style={{
                  overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap',
                  color: isErr ? 'var(--accent-red)' : 'var(--text-primary)',
                }}>
                  {e.file || e.error || e.action}
                </span>
              </div>
            )
          })}
        </div>
      </div>
    </div>
  )
}
