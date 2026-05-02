import { Navigate, Route, Routes, useLocation, useNavigate } from 'react-router-dom'
import { clearSession, getUser } from './lib/api'
import { ConfirmProvider } from './components/ConfirmDialog'
import LoadingBar from './components/LoadingBar'
import Logo from '@shared/components/Logo'
import Login from './pages/Login'
import Files from './pages/Files'
import Admin from './pages/Admin'

function Background() {
  return (
    <div className="background" aria-hidden>
      <div className="gradient gradient-1" />
      <div className="gradient gradient-2" />
      <div className="gradient gradient-3" />
    </div>
  )
}

function Nav() {
  const u = getUser()
  const nav = useNavigate()
  const loc = useLocation()
  if (!u) return null
  const activeStyle = { color: 'var(--text-primary)' }
  return (
    <div className="navbar">
      <Logo />
      <a href="/files" style={loc.pathname.startsWith('/files') ? activeStyle : undefined}>Files</a>
      {u.role === 'admin' && (
        <a href="/admin" style={loc.pathname.startsWith('/admin') ? activeStyle : undefined}>Admin</a>
      )}
      <div className="spacer" />
      <span className="muted">{u.login}</span>
      <button className="ghost" onClick={() => { clearSession(); nav('/login') }}>Logout</button>
    </div>
  )
}

function Protected({ children }: { children: React.ReactNode }) {
  if (!getUser()) return <Navigate to="/login" replace />
  return <>{children}</>
}

export default function App() {
  return (
    <ConfirmProvider>
      <div className="app">
        <LoadingBar />
        <Background />
        <Nav />
        <div className="layout">
          <Routes>
            <Route path="/login" element={<Login />} />
            <Route path="/files" element={<Protected><Files /></Protected>} />
            <Route path="/admin" element={<Protected><Admin /></Protected>} />
            <Route path="*" element={<Navigate to="/files" replace />} />
          </Routes>
        </div>
      </div>
    </ConfirmProvider>
  )
}
