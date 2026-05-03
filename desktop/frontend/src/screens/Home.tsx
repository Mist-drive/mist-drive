import { useEffect, useState } from 'react'
import { GetVersion, Me, OpenWebApp } from '../../wailsjs/go/main/App'
import { is401, notifySessionExpired } from '../session'
import { apiclient } from '../../wailsjs/go/models'
import Logo from '@shared/components/Logo'
import Files from './Files'
import SyncPanel from './Sync'

type Props = {
  user: apiclient.PublicUser
  onLogout: () => Promise<void>
}

type Tab = 'files' | 'sync'

export default function Home({ user: initial, onLogout }: Props) {
  const [user, setUser] = useState(initial)
  const [tab, setTab] = useState<Tab>('files')
  const [version, setVersion] = useState('')

  useEffect(() => {
    GetVersion().then(setVersion).catch(() => {})
  }, [])

  const refreshQuota = async () => {
    try { setUser(await Me()) } catch (e) { if (is401(e)) notifySessionExpired() }
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
        <Logo version={version || undefined} />
        <button style={tabStyle('files')} onClick={() => setTab('files')}>Files</button>
        <button style={tabStyle('sync')} onClick={() => setTab('sync')}>Sync</button>
        <button style={tabStyle('files')} onClick={() => OpenWebApp()}>Web ↗</button>
        <div className="spacer" />
        <span className="muted">{user.login}</span>
        <button className="ghost" onClick={onLogout}>Logout</button>
      </div>
      <div className="layout">
        {tab === 'files' && <Files onQuotaChange={refreshQuota} user={user} />}
        {tab === 'sync' && <SyncPanel />}
      </div>
    </div>
  )
}
