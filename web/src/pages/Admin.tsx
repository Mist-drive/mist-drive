import { useEffect, useState } from 'react'
import { api, PublicUser } from '../lib/api'
import { useConfirm } from '../components/ConfirmDialog'

const GiB = 1024 * 1024 * 1024
function fmt(n: number) {
  return `${(n / GiB).toFixed(2)} GiB`
}

export default function Admin() {
  const [users, setUsers] = useState<PublicUser[]>([])
  const [login, setLogin] = useState('')
  const [password, setPassword] = useState('')
  const [quotaGiB, setQuotaGiB] = useState(10)
  const [err, setErr] = useState<string | null>(null)
  const confirm = useConfirm()

  const refresh = async () => {
    try { setUsers(await api.admin.listUsers()) }
    catch (e: any) { setErr(e.message) }
  }
  useEffect(() => { refresh() }, [])

  const create = async (e: React.FormEvent) => {
    e.preventDefault()
    setErr(null)
    try {
      await api.admin.createUser(login, password, quotaGiB * GiB)
      setLogin(''); setPassword('')
      refresh()
    } catch (e: any) { setErr(e.message) }
  }

  const changeQuota = async (id: string, gib: number) => {
    await api.admin.patchQuota(id, gib * GiB)
    refresh()
  }

  const remove = async (u: PublicUser) => {
    const ok = await confirm({
      title: `Delete user ${u.login}`,
      message:
        `This will permanently delete user "${u.login}" and their entire bucket ` +
        `(${fmt(u.usedBytes)} of data). This cannot be undone.`,
      confirmText: 'Delete user',
      danger: true,
    })
    if (!ok) return
    await api.admin.deleteUser(u.id)
    refresh()
  }

  return (
    <div>
      <form className="card" onSubmit={create}>
        <h3>Create user</h3>
        <div className="row">
          <input placeholder="login" value={login} onChange={e => setLogin(e.target.value)} />
          <input placeholder="password" type="password" value={password} onChange={e => setPassword(e.target.value)} />
          <input placeholder="quota GiB" type="number" value={quotaGiB} onChange={e => setQuotaGiB(parseInt(e.target.value) || 0)} />
          <button>Create</button>
        </div>
      </form>
      {err && <p style={{ color: 'salmon' }}>{err}</p>}
      <table>
        <thead><tr><th>Login</th><th>Role</th><th>Used / Quota</th><th>Quota (GiB)</th><th></th></tr></thead>
        <tbody>
          {users.map(u => (
            <tr key={u.id}>
              <td>{u.login}</td>
              <td><span className={`badge ${u.role}`}>{u.role}</span></td>
              <td>{fmt(u.usedBytes)} / {fmt(u.quotaBytes)}</td>
              <td>
                <input type="number" defaultValue={Math.round(u.quotaBytes / GiB)}
                  onBlur={e => changeQuota(u.id, parseInt(e.target.value) || 0)} />
              </td>
              <td>{u.role !== 'admin' && <button className="danger" onClick={() => remove(u)}>Delete</button>}</td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  )
}
