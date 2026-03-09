import { useState, useEffect, useCallback } from 'react'
import { listUsers, createUserApi, deleteUserApi, updatePasswordApi } from '../api.ts'
import type { UserRecord } from '../types.ts'
import { useAuth } from '../context/AuthContext.tsx'

export default function UsersTab() {
  const { user: currentUser } = useAuth()
  const [users, setUsers] = useState<UserRecord[]>([])
  const [loading, setLoading] = useState(true)
  const [toast, setToast] = useState('')
  const [error, setError] = useState('')

  // Create form state
  const [newUsername, setNewUsername] = useState('')
  const [newPassword, setNewPassword] = useState('')
  const [newRole, setNewRole] = useState<'user' | 'admin'>('user')
  const [creating, setCreating] = useState(false)

  // Change password state
  const [pwTarget, setPwTarget] = useState('')
  const [newPw, setNewPw] = useState('')
  const [changingPw, setChangingPw] = useState(false)

  const notify = (msg: string) => {
    setToast(msg)
    setTimeout(() => setToast(''), 3000)
  }

  const load = useCallback(async () => {
    try {
      const data = await listUsers()
      setUsers(data)
      setError('')
    } catch (e: unknown) {
      setError((e as Error).message)
    } finally {
      setLoading(false)
    }
  }, [])

  useEffect(() => { load() }, [load])

  const handleCreate = async (e: React.FormEvent) => {
    e.preventDefault()
    if (!newUsername || !newPassword) return
    setCreating(true)
    try {
      await createUserApi(newUsername, newPassword, newRole)
      setNewUsername('')
      setNewPassword('')
      setNewRole('user')
      notify(`User "${newUsername}" created`)
      load()
    } catch (e: unknown) {
      setError((e as Error).message)
    } finally {
      setCreating(false)
    }
  }

  const handleDelete = async (username: string) => {
    if (!confirm(`Delete user "${username}"?`)) return
    try {
      await deleteUserApi(username)
      notify(`User "${username}" deleted`)
      load()
    } catch (e: unknown) {
      setError((e as Error).message)
    }
  }

  const handleChangePassword = async (e: React.FormEvent) => {
    e.preventDefault()
    if (!pwTarget || !newPw) return
    setChangingPw(true)
    try {
      await updatePasswordApi(pwTarget, newPw)
      setPwTarget('')
      setNewPw('')
      notify(`Password updated for "${pwTarget}"`)
    } catch (e: unknown) {
      setError((e as Error).message)
    } finally {
      setChangingPw(false)
    }
  }

  return (
    <div className="space-y-6 max-w-2xl">
      <div className="flex items-center gap-3">
        <h2 className="text-sm font-semibold text-zinc-700 dark:text-zinc-300">User Management</h2>
        <button
          onClick={load}
          className="text-xs text-zinc-500 hover:text-zinc-800 dark:hover:text-zinc-200 transition-colors"
        >
          Refresh
        </button>
        {toast && <span className="text-green-600 dark:text-green-400 text-xs ml-auto">{toast}</span>}
      </div>

      {error && (
        <p className="text-red-600 dark:text-red-400 text-xs">{error}</p>
      )}

      {/* User list */}
      <div className="border border-zinc-200 dark:border-zinc-800 rounded">
        {loading ? (
          <div className="p-4 text-zinc-500 text-xs">Loading…</div>
        ) : users.length === 0 ? (
          <div className="p-4 text-zinc-500 text-xs">No users found.</div>
        ) : (
          <table className="w-full text-xs border-collapse">
            <thead>
              <tr className="border-b border-zinc-200 dark:border-zinc-800 text-zinc-500 dark:text-zinc-500">
                <th className="px-3 py-2 text-left font-medium">Username</th>
                <th className="px-3 py-2 text-left font-medium">Role</th>
                <th className="px-3 py-2 text-left font-medium">Actions</th>
              </tr>
            </thead>
            <tbody>
              {users.map((u) => (
                <tr key={u.username} className="border-b border-zinc-100 dark:border-zinc-800 last:border-0">
                  <td className="px-3 py-2 font-mono text-zinc-800 dark:text-zinc-200">
                    {u.username}
                    {u.username === currentUser?.username && (
                      <span className="ml-1 text-zinc-400 dark:text-zinc-600">(you)</span>
                    )}
                  </td>
                  <td className="px-3 py-2">
                    <span className={`px-1.5 py-0.5 rounded text-xs font-medium ${
                      u.role === 'admin'
                        ? 'bg-green-100 dark:bg-green-900/30 text-green-700 dark:text-green-400'
                        : 'bg-zinc-100 dark:bg-zinc-800 text-zinc-600 dark:text-zinc-400'
                    }`}>
                      {u.role}
                    </span>
                  </td>
                  <td className="px-3 py-2">
                    {u.username !== currentUser?.username && (
                      <button
                        onClick={() => handleDelete(u.username)}
                        className="text-red-500 hover:text-red-700 dark:hover:text-red-400 transition-colors"
                      >
                        Delete
                      </button>
                    )}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </div>

      {/* Create user */}
      <div className="border border-zinc-200 dark:border-zinc-800 rounded p-4">
        <h3 className="text-xs font-semibold text-zinc-600 dark:text-zinc-400 mb-3">Add User</h3>
        <form onSubmit={handleCreate} className="flex items-end gap-2 flex-wrap">
          <div className="flex flex-col gap-1">
            <label className="text-xs text-zinc-500">Username</label>
            <input
              value={newUsername}
              onChange={(e) => setNewUsername(e.target.value)}
              placeholder="username"
              className="px-2 py-1 text-xs border border-zinc-300 dark:border-zinc-700 rounded bg-white dark:bg-zinc-800 text-zinc-900 dark:text-zinc-100 focus:outline-none focus:ring-1 focus:ring-green-500"
            />
          </div>
          <div className="flex flex-col gap-1">
            <label className="text-xs text-zinc-500">Password</label>
            <input
              type="password"
              value={newPassword}
              onChange={(e) => setNewPassword(e.target.value)}
              placeholder="password"
              className="px-2 py-1 text-xs border border-zinc-300 dark:border-zinc-700 rounded bg-white dark:bg-zinc-800 text-zinc-900 dark:text-zinc-100 focus:outline-none focus:ring-1 focus:ring-green-500"
            />
          </div>
          <div className="flex flex-col gap-1">
            <label className="text-xs text-zinc-500">Role</label>
            <select
              value={newRole}
              onChange={(e) => setNewRole(e.target.value as 'user' | 'admin')}
              className="px-2 py-1 text-xs border border-zinc-300 dark:border-zinc-700 rounded bg-white dark:bg-zinc-800 text-zinc-900 dark:text-zinc-100 focus:outline-none focus:ring-1 focus:ring-green-500"
            >
              <option value="user">user</option>
              <option value="admin">admin</option>
            </select>
          </div>
          <button
            type="submit"
            disabled={creating || !newUsername || !newPassword}
            className="px-3 py-1 text-xs bg-green-600 hover:bg-green-700 disabled:opacity-40 text-white rounded transition-colors"
          >
            {creating ? 'Creating…' : 'Add'}
          </button>
        </form>
      </div>

      {/* Change password */}
      <div className="border border-zinc-200 dark:border-zinc-800 rounded p-4">
        <h3 className="text-xs font-semibold text-zinc-600 dark:text-zinc-400 mb-3">Change Password</h3>
        <form onSubmit={handleChangePassword} className="flex items-end gap-2 flex-wrap">
          <div className="flex flex-col gap-1">
            <label className="text-xs text-zinc-500">Username</label>
            <select
              value={pwTarget}
              onChange={(e) => setPwTarget(e.target.value)}
              className="px-2 py-1 text-xs border border-zinc-300 dark:border-zinc-700 rounded bg-white dark:bg-zinc-800 text-zinc-900 dark:text-zinc-100 focus:outline-none focus:ring-1 focus:ring-green-500"
            >
              <option value="">— select user —</option>
              {users.map((u) => (
                <option key={u.username} value={u.username}>{u.username}</option>
              ))}
            </select>
          </div>
          <div className="flex flex-col gap-1">
            <label className="text-xs text-zinc-500">New Password</label>
            <input
              type="password"
              value={newPw}
              onChange={(e) => setNewPw(e.target.value)}
              placeholder="new password"
              className="px-2 py-1 text-xs border border-zinc-300 dark:border-zinc-700 rounded bg-white dark:bg-zinc-800 text-zinc-900 dark:text-zinc-100 focus:outline-none focus:ring-1 focus:ring-green-500"
            />
          </div>
          <button
            type="submit"
            disabled={changingPw || !pwTarget || !newPw}
            className="px-3 py-1 text-xs bg-zinc-700 hover:bg-zinc-600 disabled:opacity-40 text-white rounded transition-colors"
          >
            {changingPw ? 'Updating…' : 'Update'}
          </button>
        </form>
      </div>
    </div>
  )
}
