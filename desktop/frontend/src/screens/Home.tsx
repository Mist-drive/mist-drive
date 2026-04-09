import { useState } from 'react'
import { Me, OpenWebApp } from '../../wailsjs/go/main/App'
import { apiclient } from '../../wailsjs/go/models'
import Files from './Files'
import SyncPanel from './Sync'

function fmt(n: number) {
  const u = ['B','KB','MB','GB','TB']; let i = 0
  while (n >= 1024 && i < u.length - 1) { n /= 1024; i++ }
  return `${n.toFixed(1)} ${u[i]}`
}

type Props = {
  user: apiclient.PublicUser
  onLogout: () => Promise<void>
}

type Tab = 'files' | 'sync'

export default function Home({ user: initial, onLogout }: Props) {
  const [user, setUser] = useState(initial)
  const [tab, setTab] = useState<Tab>('files')

  const refreshQuota = async () => {
    try { setUser(await Me()) } catch { /* ignore */ }
  }

  const tabStyle = (t: Tab): React.CSSProperties => ({
    color: tab === t ? 'var(--text-primary)' : 'var(--text-secondary)',
    cursor: 'pointer',
    fontSize: '0.9rem',
    fontWeight: 500,
    textTransform: 'uppercase',
    letterSpacing: 1,
    background: 'none',
    border: 'none',
    padding: 0,
  })

  return (
    <div className="home">
      <div className="navbar">
        <div className="logo">
          <span className="logo-dot" />
          <span className="logo-text">MIST&nbsp;DRIVE</span>
        </div>
        <button style={tabStyle('files')} onClick={() => setTab('files')}>Files</button>
        <button style={tabStyle('sync')} onClick={() => setTab('sync')}>Sync</button>
        <button style={tabStyle('files')} onClick={() => OpenWebApp()}>Web ↗</button>
        <div className="spacer" />
        <span className="muted">
          {user.login} · {fmt(user.usedBytes)} / {fmt(user.quotaBytes)}
        </span>
        <button className="ghost" onClick={onLogout}>Logout</button>
      </div>
      <div className="layout">
        {tab === 'files' && <Files onQuotaChange={refreshQuota} />}
        {tab === 'sync' && <SyncPanel />}
      </div>
    </div>
  )
}
