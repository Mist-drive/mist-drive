import { useEffect, useState } from 'react'
import {
  AddSyncFolder,
  GetSettings,
  RemoveSyncFolder,
  SetBandwidthLimits,
  SetFolderDirections,
  SetFolderEnabled,
  SyncStatus,
} from '../../wailsjs/go/main/App'
import { settings, sync } from '../../wailsjs/go/models'

// Sync control panel: lists folder mappings, lets the user add/remove
// them, starts/stops the engine, tweaks bandwidth. Status is polled
// every second while running (the engine could emit events via
// runtime.EventsEmit but a simple poll is one API call and doesn't
// require any subscription lifecycle).
export default function SyncPanel() {
  const [s, setS] = useState<settings.Settings | null>(null)
  const [status, setStatus] = useState<sync.Status | null>(null)
  const [err, setErr] = useState<string | null>(null)

  const refreshSettings = async () => setS(await GetSettings())
  const refreshStatus = async () => {
    try { setStatus(await SyncStatus()) } catch { /* ignore */ }
  }

  useEffect(() => { refreshSettings(); refreshStatus() }, [])

  useEffect(() => {
    const t = setInterval(refreshStatus, 1000)
    return () => clearInterval(t)
  }, [])

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
    if (!confirm('Remove this sync folder? Local files are kept on disk.')) return
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
        <h3 style={{ margin: 0 }}>Sync</h3>
      </div>

      {err && <p className="error" style={{ marginBottom: '.8rem' }}>{err}</p>}

      {/* Engine-level activity line. The loop is always running while
          logged in, so we show counters + current file only — no more
          running/stopped state (per-folder Sync buttons own that). */}
      {status && (
        <p className="muted" style={{ marginBottom: '1rem', fontSize: '0.85rem' }}>
          ↑ {status.uploaded} ↓ {status.downloaded} = {status.skipped}
          {status.inFlight && <> · {status.inFlight}</>}
          {status.lastError && <> · <span style={{ color: 'var(--accent-red)' }}>{status.lastError}</span></>}
        </p>
      )}

      <h4 style={{
        fontSize: '0.75rem',
        textTransform: 'uppercase',
        letterSpacing: 1,
        color: 'var(--text-secondary)',
        marginBottom: '.5rem',
      }}>Folders</h4>
      {s.folders.length === 0 && (
        <p className="muted" style={{ fontSize: '0.85rem', marginBottom: '.8rem' }}>
          No sync folders yet. Add one below.
        </p>
      )}
      {s.folders.map((f, i) => {
        const up = f.upload
        const down = f.download
        const enabled = f.enabled
        const flip = async (nextUp: boolean, nextDown: boolean) => {
          await SetFolderDirections(i, nextUp, nextDown)
          await refreshSettings()
        }
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
                ⇄ {f.remotePrefix || '(bucket root)'}
              </div>
            </div>
            <div className="row" style={{ gap: '.4rem' }}>
              <button
                className="ghost"
                onClick={flipEnabled}
                title={enabled ? 'Syncing — click to pause' : 'Paused — click to sync'}
                style={{
                  padding: '.3rem .7rem',
                  fontSize: '0.8rem',
                  fontWeight: 600,
                  color: '#fff',
                  background: enabled ? 'var(--accent-green, #2ea043)' : 'var(--accent-red, #d14343)',
                  borderColor: enabled ? 'var(--accent-green, #2ea043)' : 'var(--accent-red, #d14343)',
                }}
              >Sync</button>
              <DirectionToggle
                icon="↑"
                tooltip={up ? 'Upload enabled — click to disable' : 'Upload disabled — click to enable'}
                active={up}
                onClick={() => flip(!up, down)}
              />
              <DirectionToggle
                icon="↓"
                tooltip={down ? 'Download enabled — click to disable' : 'Download disabled — click to enable'}
                active={down}
                onClick={() => flip(up, !down)}
              />
              <button className="danger" onClick={() => onRemove(i)}
                style={{ padding: '.3rem .6rem' }}>Remove</button>
            </div>
          </div>
        )
      })}

      <div className="row" style={{ marginTop: '.8rem' }}>
        <button className="ghost" onClick={onAdd}>Add folder…</button>
      </div>

      <h4 style={{
        fontSize: '0.75rem',
        textTransform: 'uppercase',
        letterSpacing: 1,
        color: 'var(--text-secondary)',
        marginTop: '1.5rem',
        marginBottom: '.5rem',
      }}>Bandwidth</h4>
      <BandwidthForm
        concurrent={s.maxConcurrentUploads}
        kbps={s.maxUploadRateKBps}
        onSave={onSaveLimits}
      />
    </div>
  )
}

// Direction toggle: a small icon button that shows whether upload or
// download is enabled. Active = accent color, inactive = muted.
function DirectionToggle({
  icon, tooltip, active, onClick,
}: { icon: string; tooltip: string; active: boolean; onClick: () => void }) {
  return (
    <button
      className="ghost"
      onClick={onClick}
      title={tooltip}
      style={{
        padding: '.3rem .55rem',
        fontSize: '0.95rem',
        lineHeight: 1,
        opacity: active ? 1 : 0.4,
        borderColor: active ? 'var(--accent)' : 'var(--border)',
        color: active ? 'var(--accent)' : 'var(--text-secondary)',
      }}
    >
      {icon}
    </button>
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
  const [mc, setMc] = useState(concurrent)
  const [kb, setKb] = useState(kbps)
  useEffect(() => { setMc(concurrent); setKb(kbps) }, [concurrent, kbps])

  return (
    <div className="row" style={{ gap: '.8rem', flexWrap: 'wrap' }}>
      <div style={{ flex: '1 1 180px' }}>
        <label>Parallel uploads</label>
        <input
          type="number"
          min={1}
          value={mc}
          onChange={(e) => setMc(parseInt(e.target.value || '1', 10))}
        />
      </div>
      <div style={{ flex: '1 1 180px' }}>
        <label>Max upload rate (KB/s, 0 = ∞)</label>
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
      >Save</button>
    </div>
  )
}
