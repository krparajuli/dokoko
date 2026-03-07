import { useState, useEffect, useCallback } from 'react'
import {
  listNetworks, createNetwork, removeNetwork,
  pruneNetworks, refreshNetworks, inspectNetwork,
} from '../api.ts'
import OpModal from './OpModal.tsx'
import InspectModal from './InspectModal.tsx'

interface NetworkRecord {
  ID?: string
  Name?: string
  Driver?: string
  Scope?: string
  Status?: string
}

type ModalType = 'create' | null

export default function NetworksTab() {
  const [networks, setNetworks] = useState<NetworkRecord[]>([])
  const [loading, setLoading] = useState(false)
  const [modal, setModal] = useState<ModalType>(null)
  const [inspectData, setInspectData] = useState<unknown>(null)
  const [selected, setSelected] = useState<NetworkRecord | null>(null)
  const [toast, setToast] = useState('')

  const load = useCallback(async () => {
    setLoading(true)
    try {
      const data = await listNetworks() as NetworkRecord[]
      setNetworks(data ?? [])
    } finally {
      setLoading(false)
    }
  }, [])

  useEffect(() => { load() }, [load])

  const notify = (msg: string) => { setToast(msg); setTimeout(() => setToast(''), 3000) }

  const act = async (fn: () => Promise<unknown>, msg: string) => {
    await fn(); notify(msg); await load()
  }

  const handleInspect = async (n: NetworkRecord) => {
    if (!n.ID && !n.Name) return
    setSelected(n)
    const data = await inspectNetwork(n.ID ?? n.Name ?? '')
    setInspectData(data)
  }

  return (
    <div className="space-y-4">
      <div className="flex items-center gap-2 flex-wrap">
        <Btn green onClick={() => setModal('create')}>Create</Btn>
        <Btn onClick={() => act(pruneNetworks, 'Prune dispatched')}>Prune unused</Btn>
        <Btn onClick={() => act(refreshNetworks, 'Network store refreshed')}>Refresh store</Btn>
        <Btn onClick={load} dim>↺ Reload</Btn>
        {toast && <span className="text-green-600 dark:text-green-400 text-xs ml-auto">{toast}</span>}
      </div>

      <table className="w-full text-xs border-collapse">
        <thead>
          <tr className="border-b border-zinc-200 dark:border-zinc-800 text-zinc-500">
            <Th>ID</Th><Th>Name</Th><Th>Driver</Th><Th>Scope</Th><Th>Status</Th><Th>Actions</Th>
          </tr>
        </thead>
        <tbody>
          {loading && <tr><td colSpan={6} className="py-6 text-center text-zinc-400 dark:text-zinc-600">Loading…</td></tr>}
          {!loading && networks.length === 0 && (
            <tr><td colSpan={6} className="py-6 text-center text-zinc-400 dark:text-zinc-600">No networks in store</td></tr>
          )}
          {networks.map((n, i) => (
            <tr key={n.ID ?? i} className="border-b border-zinc-100 dark:border-zinc-800/50 hover:bg-zinc-50 dark:hover:bg-zinc-900">
              <Td><code className="text-yellow-600 dark:text-yellow-400">{(n.ID ?? '').slice(0, 12) || '—'}</code></Td>
              <Td><span className="text-cyan-600 dark:text-cyan-400">{n.Name ?? '—'}</span></Td>
              <Td>{n.Driver ?? '—'}</Td>
              <Td>{n.Scope ?? '—'}</Td>
              <Td>{n.Status ?? '—'}</Td>
              <Td>
                <div className="flex gap-2">
                  <button
                    onClick={() => act(() => removeNetwork(n.ID ?? n.Name!), 'Remove dispatched')}
                    className="text-red-600 dark:text-red-400 hover:text-red-700 dark:hover:text-red-300 underline"
                  >
                    rm
                  </button>
                  <button
                    onClick={() => handleInspect(n)}
                    className="text-zinc-500 dark:text-zinc-400 hover:text-zinc-800 dark:hover:text-zinc-200 underline"
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
          title="Create Network"
          fields={[
            { key: 'name', label: 'Network name', required: true, placeholder: 'my-network' },
            { key: 'driver', label: 'Driver', placeholder: 'bridge', defaultValue: 'bridge' },
          ]}
          onSubmit={async (v) => { await createNetwork(v.name!, v.driver); await load() }}
          onClose={() => setModal(null)}
        />
      )}

      {inspectData !== null && (
        <InspectModal
          title={`Network: ${selected?.Name ?? selected?.ID?.slice(0, 12)}`}
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
        green ? 'bg-green-600 hover:bg-green-500 dark:bg-green-700 dark:hover:bg-green-600 text-white'
        : dim  ? 'bg-zinc-100 dark:bg-zinc-800 hover:bg-zinc-200 dark:hover:bg-zinc-700 text-zinc-500'
        :         'bg-zinc-100 dark:bg-zinc-800 hover:bg-zinc-200 dark:hover:bg-zinc-700 text-zinc-800 dark:text-zinc-200'
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
