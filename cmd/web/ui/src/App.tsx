import { useState, useEffect, useCallback } from 'react'
import Header from './components/Header.tsx'
import ImagesTab from './components/ImagesTab.tsx'
import ContainersTab from './components/ContainersTab.tsx'
import VolumesTab from './components/VolumesTab.tsx'
import NetworksTab from './components/NetworksTab.tsx'
import ExecsTab from './components/ExecsTab.tsx'
import TerminalTab from './components/TerminalTab.tsx'
import UsersTab from './components/UsersTab.tsx'
import LogsPanel from './components/LogsPanel.tsx'
import LoginPage from './components/LoginPage.tsx'
import RegisterPage from './components/RegisterPage.tsx'
import { AuthProvider, useAuth } from './context/AuthContext.tsx'
import { health } from './api.ts'
import type { Tab, HealthStatus } from './types.ts'

function AppInner() {
  const { user, loading } = useAuth()
  const [authView, setAuthView] = useState<'login' | 'register'>('login')
  const [activeTab, setActiveTab] = useState<Tab>('images')
  const [dockerStatus, setDockerStatus] = useState<HealthStatus>({ ok: false, docker: false })
  const [viewMode, setViewMode] = useState<'admin' | 'user'>(
    () => (user?.role === 'admin' ? 'admin' : 'user')
  )

  const checkHealth = useCallback(async () => {
    try {
      const status = await health()
      setDockerStatus(status)
    } catch {
      setDockerStatus({ ok: false, docker: false, error: 'server unreachable' })
    }
  }, [])

  useEffect(() => {
    if (!user) return
    checkHealth()
    const id = setInterval(checkHealth, 10_000)
    return () => clearInterval(id)
  }, [checkHealth, user])

  // Reset viewMode when user changes
  useEffect(() => {
    setViewMode(user?.role === 'admin' ? 'admin' : 'user')
  }, [user])

  if (loading) {
    return (
      <div className="flex items-center justify-center min-h-screen bg-zinc-100 dark:bg-zinc-950">
        <span className="inline-block w-6 h-6 border-2 border-zinc-300 dark:border-zinc-600 border-t-green-500 dark:border-t-green-400 rounded-full animate-spin" />
      </div>
    )
  }

  if (!user) {
    return authView === 'register'
      ? <RegisterPage onShowLogin={() => setAuthView('login')} />
      : <LoginPage onShowRegister={() => setAuthView('register')} />
  }

  // In user view, force terminal tab
  const effectiveTab = viewMode === 'user' ? 'terminal' : activeTab

  return (
    <div className="flex flex-col h-screen overflow-hidden">
      <Header
        activeTab={effectiveTab}
        onTabChange={setActiveTab}
        dockerStatus={dockerStatus}
        viewMode={viewMode}
        onViewModeChange={setViewMode}
      />

      <main className="flex-1 overflow-auto p-4">
        {effectiveTab === 'images'     && <ImagesTab />}
        {effectiveTab === 'containers' && <ContainersTab />}
        {effectiveTab === 'volumes'    && <VolumesTab />}
        {effectiveTab === 'networks'   && <NetworksTab />}
        {effectiveTab === 'execs'      && <ExecsTab />}
        {effectiveTab === 'terminal'   && <TerminalTab />}
        {effectiveTab === 'users'      && <UsersTab />}
      </main>

      <LogsPanel />
    </div>
  )
}

export default function App() {
  return (
    <AuthProvider>
      <AppInner />
    </AuthProvider>
  )
}
