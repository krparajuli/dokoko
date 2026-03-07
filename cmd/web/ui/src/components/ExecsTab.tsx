import { useState } from 'react'
import { createExec, startExec, inspectExec } from '../api.ts'
import OpModal from './OpModal.tsx'
import InspectModal from './InspectModal.tsx'

interface ExecEntry {
  id: string
  container: string
  cmd: string
  createdAt: string
}

type ModalType = 'create' | 'start' | 'inspect' | null

export default function ExecsTab() {
  const [execs, setExecs] = useState<ExecEntry[]>([])
  const [modal, setModal] = useState<ModalType>(null)
  const [selectedId, setSelectedId] = useState('')
  const [inspectData, setInspectData] = useState<unknown>(null)
  const [toast, setToast] = useState('')

  const notify = (msg: string) => { setToast(msg); setTimeout(() => setToast(''), 3000) }

  const handleInspect = async (id: string) => {
    const data = await inspectExec(id)
    setInspectData(data)
  }

  return (
    <div className="space-y-4">
      <div className="flex items-center gap-2 flex-wrap">
        <Btn green onClick={() => setModal('create')}>Create exec</Btn>
        <Btn onClick={() => setModal('start')}>Start exec</Btn>
        <Btn onClick={() => setModal('inspect')}>Inspect exec</Btn>
        {toast && <span className="text-green-600 dark:text-green-400 text-xs ml-auto">{toast}</span>}
      </div>

      {execs.length === 0 ? (
        <div className="text-zinc-500 dark:text-zinc-600 text-sm py-8 text-center">
          No exec sessions yet. Use "Create exec" to run a command in a container.
        </div>
      ) : (
        <table className="w-full text-xs border-collapse">
          <thead>
            <tr className="border-b border-zinc-200 dark:border-zinc-800 text-zinc-500">
              <Th>Exec ID</Th><Th>Container</Th><Th>Cmd</Th><Th>Created</Th><Th>Actions</Th>
            </tr>
          </thead>
          <tbody>
            {execs.map((e) => (
              <tr key={e.id} className="border-b border-zinc-100 dark:border-zinc-800/50 hover:bg-zinc-50 dark:hover:bg-zinc-900">
                <Td><code className="text-yellow-600 dark:text-yellow-400">{e.id.slice(0, 16)}</code></Td>
                <Td className="text-cyan-600 dark:text-cyan-400">{e.container}</Td>
                <Td className="text-zinc-700 dark:text-zinc-300">{e.cmd}</Td>
                <Td className="text-zinc-500">{e.createdAt}</Td>
                <Td>
                  <div className="flex gap-2">
                    <button
                      onClick={() => { setSelectedId(e.id); startExec(e.id, true).then(() => notify('Exec started')) }}
                      className="text-green-600 dark:text-green-400 hover:text-green-700 dark:hover:text-green-300 underline"
                    >
                      start
                    </button>
                    <button
                      onClick={() => { setSelectedId(e.id); handleInspect(e.id) }}
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
      )}

      {modal === 'create' && (
        <OpModal
          title="Create Exec"
          fields={[
            { key: 'container', label: 'Container ID or name', required: true },
            { key: 'cmd', label: 'Command', placeholder: '/bin/sh', defaultValue: '/bin/sh' },
          ]}
          onSubmit={async (v) => {
            await createExec(v.container!, v.cmd ?? '/bin/sh')
            setExecs((prev) => [
              ...prev,
              { id: `exec-${Date.now()}`, container: v.container!, cmd: v.cmd!, createdAt: new Date().toISOString() },
            ])
            notify('Exec created')
          }}
          onClose={() => setModal(null)}
        />
      )}

      {modal === 'start' && (
        <OpModal
          title="Start Exec"
          fields={[
            { key: 'id', label: 'Exec ID', required: true, defaultValue: selectedId },
          ]}
          onSubmit={async (v) => { await startExec(v.id!, true); notify('Exec started') }}
          onClose={() => setModal(null)}
        />
      )}

      {modal === 'inspect' && (
        <OpModal
          title="Inspect Exec"
          fields={[
            { key: 'id', label: 'Exec ID', required: true, defaultValue: selectedId },
          ]}
          onSubmit={async (v) => { await handleInspect(v.id!); setModal(null) }}
          onClose={() => setModal(null)}
        />
      )}

      {inspectData !== null && (
        <InspectModal
          title="Exec inspect"
          data={inspectData}
          onClose={() => { setInspectData(null) }}
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
