import { useState, useEffect, useCallback } from 'react'
import {
  listVolumes, createVolume, removeVolume,
  pruneVolumes, refreshVolumes, inspectVolume,
} from '../api.ts'
import OpModal from './OpModal.tsx'
import InspectModal from './InspectModal.tsx'

interface VolumeRecord {
  Name?: string
  Driver?: string
  Mountpoint?: string
  Scope?: string
  Status?: string
}

type ModalType = 'create' | null

export default function VolumesTab() {
  const [volumes, setVolumes] = useState<VolumeRecord[]>([])
  const [loading, setLoading] = useState(false)
  const [modal, setModal] = useState<ModalType>(null)
  const [inspectData, setInspectData] = useState<unknown>(null)
  const [selected, setSelected] = useState<VolumeRecord | null>(null)
  const [toast, setToast] = useState('')

  const load = useCallback(async () => {
    setLoading(true)
    try {
      const data = await listVolumes() as VolumeRecord[]
      setVolumes(data ?? [])
    } finally {
      setLoading(false)
    }
  }, [])

  useEffect(() => { load() }, [load])

  const notify = (msg: string) => { setToast(msg); setTimeout(() => setToast(''), 3000) }

  const act = async (fn: () => Promise<unknown>, msg: string) => {
    await fn(); notify(msg); await load()
  }

  const handleInspect = async (v: VolumeRecord) => {
    if (!v.Name) return
    setSelected(v)
    const data = await inspectVolume(v.Name)
    setInspectData(data)
  }

  return (
    <div className="space-y-4">
      <div className="flex items-center gap-2 flex-wrap">
        <Btn green onClick={() => setModal('create')}>Create</Btn>
        <Btn onClick={() => act(pruneVolumes, 'Prune dispatched')}>Prune unused</Btn>
        <Btn onClick={() => act(refreshVolumes, 'Volume store refreshed')}>Refresh store</Btn>
        <Btn onClick={load} dim>↺ Reload</Btn>
        {toast && <span className="text-green-400 text-xs ml-auto">{toast}</span>}
      </div>

      <table className="w-full text-xs border-collapse">
        <thead>
          <tr className="border-b border-zinc-800 text-zinc-500">
            <Th>Name</Th><Th>Driver</Th><Th>Mountpoint</Th><Th>Scope</Th><Th>Status</Th><Th>Actions</Th>
          </tr>
        </thead>
        <tbody>
          {loading && <tr><td colSpan={6} className="py-6 text-center text-zinc-600">Loading…</td></tr>}
          {!loading && volumes.length === 0 && (
            <tr><td colSpan={6} className="py-6 text-center text-zinc-600">No volumes in store</td></tr>
          )}
          {volumes.map((v, i) => (
            <tr key={v.Name ?? i} className="border-b border-zinc-800/50 hover:bg-zinc-900">
              <Td><span className="text-cyan-400">{v.Name ?? '—'}</span></Td>
              <Td>{v.Driver ?? '—'}</Td>
              <Td className="text-zinc-500 max-w-xs truncate">{v.Mountpoint ?? '—'}</Td>
              <Td>{v.Scope ?? '—'}</Td>
              <Td>{v.Status ?? '—'}</Td>
              <Td>
                <div className="flex gap-2">
                  <button
                    onClick={() => act(() => removeVolume(v.Name!), 'Remove dispatched')}
                    className="text-red-400 hover:text-red-300 underline"
                  >
                    rm
                  </button>
                  <button
                    onClick={() => handleInspect(v)}
                    className="text-zinc-400 hover:text-zinc-200 underline"
                  >
                    inspect
                  </button>
                </div>
              </Td>
            </tr>
          ))}
        </tbody>
      </table>

      {modal === 'create' && (
        <OpModal
          title="Create Volume"
          fields={[
            { key: 'name', label: 'Volume name', placeholder: 'my-volume' },
            { key: 'driver', label: 'Driver', placeholder: 'local', defaultValue: 'local' },
          ]}
          onSubmit={async (v) => { await createVolume(v.name!, v.driver); await load() }}
          onClose={() => setModal(null)}
        />
      )}

      {inspectData !== null && (
        <InspectModal
          title={`Volume: ${selected?.Name}`}
          data={inspectData}
          onClose={() => { setInspectData(null); setSelected(null) }}
        />
      )}
    </div>
  )
}

function Btn({ children, onClick, green, dim }: { children: React.ReactNode; onClick?: () => void; green?: boolean; dim?: boolean }) {
  return (
    <button
      onClick={onClick}
      className={`px-3 py-1.5 rounded text-xs font-medium transition-colors ${
        green ? 'bg-green-700 hover:bg-green-600 text-white'
        : dim  ? 'bg-zinc-800 hover:bg-zinc-700 text-zinc-500'
        :         'bg-zinc-800 hover:bg-zinc-700 text-zinc-200'
      }`}
    >
      {children}
    </button>
  )
}

function Th({ children }: { children: React.ReactNode }) {
  return <th className="text-left px-3 py-2 font-medium">{children}</th>
}

function Td({ children, className = '' }: { children: React.ReactNode; className?: string }) {
  return <td className={`px-3 py-2 ${className}`}>{children}</td>
}
