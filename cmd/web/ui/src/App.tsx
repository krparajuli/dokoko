import { useState, useEffect, useCallback } from 'react'
import Header from './components/Header.tsx'
import ImagesTab from './components/ImagesTab.tsx'
import ContainersTab from './components/ContainersTab.tsx'
import VolumesTab from './components/VolumesTab.tsx'
import NetworksTab from './components/NetworksTab.tsx'
import ExecsTab from './components/ExecsTab.tsx'
import LogsPanel from './components/LogsPanel.tsx'
import { health } from './api.ts'
import type { Tab, HealthStatus } from './types.ts'

export default function App() {
  const [activeTab, setActiveTab] = useState<Tab>('images')
  const [dockerStatus, setDockerStatus] = useState<HealthStatus>({ ok: false, docker: false })

  const checkHealth = useCallback(async () => {
    try {
      const status = await health()
      setDockerStatus(status)
    } catch {
      setDockerStatus({ ok: false, docker: false, error: 'server unreachable' })
    }
  }, [])

  useEffect(() => {
    checkHealth()
    const id = setInterval(checkHealth, 10_000)
    return () => clearInterval(id)
  }, [checkHealth])

  return (
    <div className="flex flex-col h-screen overflow-hidden">
      <Header activeTab={activeTab} onTabChange={setActiveTab} dockerStatus={dockerStatus} />

      <main className="flex-1 overflow-auto p-4">
        {activeTab === 'images'     && <ImagesTab />}
        {activeTab === 'containers' && <ContainersTab />}
        {activeTab === 'volumes'    && <VolumesTab />}
        {activeTab === 'networks'   && <NetworksTab />}
        {activeTab === 'execs'      && <ExecsTab />}
      </main>

      <LogsPanel />
    </div>
  )
}
