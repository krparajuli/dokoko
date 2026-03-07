import { useState, useEffect, useCallback } from 'react'
import { listImages, pullImage, removeImage, tagImage, refreshImages, inspectImage } from '../api.ts'
import type { ImageRecord } from '../types.ts'
import OpModal from './OpModal.tsx'
import InspectModal from './InspectModal.tsx'

type ModalType = 'pull' | 'remove' | 'tag' | null

function fmtBytes(n: number): string {
  if (n < 1024) return `${n} B`
  if (n < 1024 * 1024) return `${(n / 1024).toFixed(1)} KB`
  if (n < 1024 * 1024 * 1024) return `${(n / 1024 / 1024).toFixed(1)} MB`
  return `${(n / 1024 / 1024 / 1024).toFixed(2)} GB`
}

function statusColor(s: string) {
  if (s === 'present') return 'text-green-400'
  if (s === 'deleted') return 'text-red-400'
  if (s === 'deleted_out_of_band') return 'text-orange-400'
  if (s === 'errored') return 'text-red-500'
  return 'text-zinc-400'
}

export default function ImagesTab() {
  const [images, setImages] = useState<ImageRecord[]>([])
  const [loading, setLoading] = useState(false)
  const [modal, setModal] = useState<ModalType>(null)
  const [selected, setSelected] = useState<ImageRecord | null>(null)
  const [inspectData, setInspectData] = useState<unknown>(null)
  const [toast, setToast] = useState('')

  const load = useCallback(async () => {
    setLoading(true)
    try {
      const data = await listImages() as ImageRecord[]
      setImages(data ?? [])
    } finally {
      setLoading(false)
    }
  }, [])

  useEffect(() => { load() }, [load])

  const notify = (msg: string) => {
    setToast(msg)
    setTimeout(() => setToast(''), 3000)
  }

  const handleRefresh = async () => {
    await refreshImages()
    await load()
    notify('Image store refreshed')
  }

  const handleInspect = async (img: ImageRecord) => {
    const data = await inspectImage(img.DockerID)
    setInspectData(data)
  }

  return (
    <div className="space-y-4">
      {/* Toolbar */}
      <div className="flex items-center gap-2 flex-wrap">
        <Btn green onClick={() => setModal('pull')}>Pull</Btn>
        <Btn onClick={() => setModal('remove')}>Remove</Btn>
        <Btn onClick={() => setModal('tag')}>Tag</Btn>
        <Btn onClick={handleRefresh}>Refresh store</Btn>
        <Btn onClick={load} dim>↺ Reload</Btn>
        {toast && <span className="text-green-400 text-xs ml-auto">{toast}</span>}
      </div>

      {/* Table */}
      <div className="overflow-x-auto">
        <table className="w-full text-xs border-collapse">
          <thead>
            <tr className="border-b border-zinc-800 text-zinc-500">
              <Th>ID</Th>
              <Th>Tags</Th>
              <Th>Size</Th>
              <Th>OS/Arch</Th>
              <Th>Status</Th>
              <Th>Origin</Th>
              <Th>Actions</Th>
            </tr>
          </thead>
          <tbody>
            {loading && (
              <tr><td colSpan={7} className="py-6 text-center text-zinc-600">Loading…</td></tr>
            )}
            {!loading && images.length === 0 && (
              <tr><td colSpan={7} className="py-6 text-center text-zinc-600">No images in store</td></tr>
            )}
            {images.map((img) => (
              <tr
                key={img.DockerID}
                className="border-b border-zinc-800/50 hover:bg-zinc-900 transition-colors"
              >
                <Td><code className="text-yellow-400">{img.ShortID}</code></Td>
                <Td>
                  {img.RepoTags?.length
                    ? img.RepoTags.map((t) => (
                        <span key={t} className="mr-1 text-cyan-400">{t}</span>
                      ))
                    : <span className="text-zinc-600">&lt;untagged&gt;</span>}
                </Td>
                <Td>{fmtBytes(img.Size)}</Td>
                <Td>{[img.OS, img.Architecture, img.Variant].filter(Boolean).join('/')}</Td>
                <Td><span className={statusColor(img.Status)}>{img.Status}</span></Td>
                <Td><span className="text-zinc-500">{img.Origin}</span></Td>
                <Td>
                  <button
                    onClick={() => { setSelected(img); handleInspect(img) }}
                    className="text-zinc-400 hover:text-zinc-200 underline"
                  >
                    inspect
                  </button>
                </Td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>

      {modal === 'pull' && (
        <OpModal
          title="Pull Image"
          fields={[
            { key: 'ref', label: 'Image ref (e.g. ubuntu:22.04)', placeholder: 'ubuntu:latest', required: true },
            { key: 'platform', label: 'Platform (optional)', placeholder: 'linux/amd64' },
          ]}
          onSubmit={async (v) => { await pullImage(v.ref!, v.platform); await load() }}
          onClose={() => setModal(null)}
        />
      )}

      {modal === 'remove' && (
        <OpModal
          title="Remove Image"
          fields={[
            { key: 'id', label: 'Image ID or tag', required: true,
              defaultValue: selected?.DockerID ?? '' },
          ]}
          onSubmit={async (v) => { await removeImage(v.id!); await load() }}
          onClose={() => setModal(null)}
        />
      )}

      {modal === 'tag' && (
        <OpModal
          title="Tag Image"
          fields={[
            { key: 'source', label: 'Source image', required: true,
              defaultValue: selected?.RepoTags?.[0] ?? '' },
            { key: 'target', label: 'Target tag', required: true, placeholder: 'myimage:latest' },
          ]}
          onSubmit={async (v) => { await tagImage(v.source!, v.target!); await load() }}
          onClose={() => setModal(null)}
        />
      )}

      {inspectData !== null && (
        <InspectModal
          title={`Image: ${selected?.ShortID}`}
          data={inspectData}
          onClose={() => { setInspectData(null); setSelected(null) }}
        />
      )}
    </div>
  )
}

function Btn({
  children,
  onClick,
  green,
  dim,
}: {
  children: React.ReactNode
  onClick?: () => void
  green?: boolean
  dim?: boolean
}) {
  return (
    <button
      onClick={onClick}
      className={`px-3 py-1.5 rounded text-xs font-medium transition-colors ${
        green
          ? 'bg-green-700 hover:bg-green-600 text-white'
          : dim
          ? 'bg-zinc-800 hover:bg-zinc-700 text-zinc-500'
          : 'bg-zinc-800 hover:bg-zinc-700 text-zinc-200'
      }`}
    >
      {children}
    </button>
  )
}

function Th({ children }: { children: React.ReactNode }) {
  return <th className="text-left px-3 py-2 font-medium">{children}</th>
}

function Td({ children }: { children: React.ReactNode }) {
  return <td className="px-3 py-2">{children}</td>
}
