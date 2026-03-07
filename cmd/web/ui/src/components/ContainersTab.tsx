import { useState, useEffect, useCallback } from 'react'
import {
  listContainers, createContainer, startContainer,
  stopContainer, removeContainer, inspectContainer,
} from '../api.ts'
import type { Container } from '../types.ts'
import OpModal from './OpModal.tsx'
import InspectModal from './InspectModal.tsx'

type ModalType = 'create' | null

function stateColor(s: string) {
  if (s === 'running') return 'text-green-600 dark:text-green-400'
  if (s === 'exited')  return 'text-red-600 dark:text-red-400'
  if (s === 'paused')  return 'text-yellow-600 dark:text-yellow-400'
  return 'text-zinc-500 dark:text-zinc-400'
}

export default function ContainersTab() {
  const [containers, setContainers] = useState<Container[]>([])
  const [loading, setLoading] = useState(false)
  const [modal, setModal] = useState<ModalType>(null)
  const [inspectData, setInspectData] = useState<unknown>(null)
  const [selected, setSelected] = useState<Container | null>(null)
  const [toast, setToast] = useState('')

  const load = useCallback(async () => {
    setLoading(true)
    try {
      const data = await listContainers() as Container[]
      setContainers(data ?? [])
    } finally {
      setLoading(false)
    }
  }, [])

  useEffect(() => { load() }, [load])

  const notify = (msg: string) => {
    setToast(msg)
    setTimeout(() => setToast(''), 3000)
  }

  const act = async (fn: () => Promise<unknown>, msg: string) => {
    await fn()
    notify(msg)
    await load()
  }

  const handleInspect = async (c: Container) => {
    setSelected(c)
    const data = await inspectContainer(c.Id)
    setInspectData(data)
  }

  return (
    <div className="space-y-4">
      <div className="flex items-center gap-2 flex-wrap">
        <Btn green onClick={() => setModal('create')}>Create</Btn>
        <Btn onClick={load} dim>↺ Reload</Btn>
        {toast && <span className="text-green-600 dark:text-green-400 text-xs ml-auto">{toast}</span>}
      </div>

      <div className="overflow-x-auto">
        <table className="w-full text-xs border-collapse">
          <thead>
            <tr className="border-b border-zinc-200 dark:border-zinc-800 text-zinc-500">
              <Th>ID</Th>
              <Th>Name</Th>
              <Th>Image</Th>
              <Th>State</Th>
              <Th>Status</Th>
              <Th>Actions</Th>
            </tr>
          </thead>
          <tbody>
            {loading && (
              <tr><td colSpan={6} className="py-6 text-center text-zinc-400 dark:text-zinc-600">Loading…</td></tr>
            )}
            {!loading && containers.length === 0 && (
              <tr><td colSpan={6} className="py-6 text-center text-zinc-400 dark:text-zinc-600">No containers found</td></tr>
            )}
            {containers.map((c) => (
              <tr key={c.Id} className="border-b border-zinc-100 dark:border-zinc-800/50 hover:bg-zinc-50 dark:hover:bg-zinc-900 transition-colors">
                <Td><code className="text-yellow-600 dark:text-yellow-400">{c.Id.slice(0, 12)}</code></Td>
                <Td>{c.Names?.map((n) => n.replace(/^\//, '')).join(', ') || '—'}</Td>
                <Td className="text-cyan-600 dark:text-cyan-400">{c.Image}</Td>
                <Td><span className={stateColor(c.State)}>{c.State}</span></Td>
                <Td className="text-zinc-500">{c.Status}</Td>
                <Td>
                  <div className="flex gap-2">
                    {c.State !== 'running' && (
                      <ActionBtn
                        color="green"
                        onClick={() => act(() => startContainer(c.Id), 'Start dispatched')}
                      >
                        start
                      </ActionBtn>
                    )}
                    {c.State === 'running' && (
                      <ActionBtn
                        color="yellow"
                        onClick={() => act(() => stopContainer(c.Id), 'Stop dispatched')}
                      >
                        stop
                      </ActionBtn>
                    )}
                    <ActionBtn
                      color="red"
                      onClick={() => act(() => removeContainer(c.Id), 'Remove dispatched')}
                    >
                      rm
                    </ActionBtn>
                    <ActionBtn onClick={() => handleInspect(c)}>inspect</ActionBtn>
                  </div>
                </Td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>

      {modal === 'create' && (
        <OpModal
          title="Create Container"
          fields={[
            { key: 'image', label: 'Image', required: true, placeholder: 'nginx:latest' },
            { key: 'name', label: 'Container name (optional)', placeholder: 'my-container' },
          ]}
          onSubmit={async (v) => { await createContainer(v.image!, v.name ?? ''); await load() }}
          onClose={() => setModal(null)}
        />
      )}

      {inspectData !== null && (
        <InspectModal
          title={`Container: ${selected?.Id.slice(0, 12)}`}
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

function ActionBtn({ children, onClick, color }: { children: React.ReactNode; onClick: () => void; color?: 'green' | 'yellow' | 'red' }) {
  const cls = color === 'green'  ? 'text-green-600 dark:text-green-400 hover:text-green-700 dark:hover:text-green-300'
            : color === 'yellow' ? 'text-yellow-600 dark:text-yellow-400 hover:text-yellow-700 dark:hover:text-yellow-300'
            : color === 'red'    ? 'text-red-600 dark:text-red-400 hover:text-red-700 dark:hover:text-red-300'
            : 'text-zinc-500 dark:text-zinc-400 hover:text-zinc-800 dark:hover:text-zinc-200'
  return (
    <button onClick={onClick} className={`underline ${cls}`}>{children}</button>
  )
}

function Th({ children }: { children: React.ReactNode }) {
  return <th className="text-left px-3 py-2 font-medium">{children}</th>
}

function Td({ children, className = '' }: { children: React.ReactNode; className?: string }) {
  return <td className={`px-3 py-2 ${className}`}>{children}</td>
}
