import { useState } from 'react'

export interface Field {
  key: string
  label: string
  placeholder?: string
  required?: boolean
  defaultValue?: string
}

interface Props {
  title: string
  fields: Field[]
  onSubmit: (values: Record<string, string>) => Promise<void>
  onClose: () => void
}

export default function OpModal({ title, fields, onSubmit, onClose }: Props) {
  const initial = Object.fromEntries(
    fields.map((f) => [f.key, f.defaultValue ?? '']),
  )
  const [values, setValues] = useState<Record<string, string>>(initial)
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState('')

  const handleSubmit = async (e: React.FormEvent) => {
    e.preventDefault()
    setError('')
    setLoading(true)
    try {
      await onSubmit(values)
      onClose()
    } catch (err) {
      setError(err instanceof Error ? err.message : 'operation failed')
    } finally {
      setLoading(false)
    }
  }

  return (
    <div
      className="fixed inset-0 z-50 flex items-center justify-center bg-black/60 dark:bg-black/70"
      onClick={(e) => e.target === e.currentTarget && onClose()}
    >
      <div className="bg-white dark:bg-zinc-900 border border-zinc-200 dark:border-zinc-700 rounded-lg w-full max-w-md p-6 shadow-lg">
        <h3 className="text-green-600 dark:text-green-400 font-bold text-lg mb-4">{title}</h3>

        <form onSubmit={handleSubmit} className="space-y-3">
          {fields.map((field) => (
            <div key={field.key}>
              <label className="block text-xs text-zinc-600 dark:text-zinc-400 mb-1">{field.label}</label>
              <input
                type="text"
                value={values[field.key] ?? ''}
                onChange={(e) =>
                  setValues((v) => ({ ...v, [field.key]: e.target.value }))
                }
                placeholder={field.placeholder}
                required={field.required}
                className="w-full bg-zinc-50 dark:bg-zinc-800 border border-zinc-300 dark:border-zinc-700 rounded px-3 py-2 text-sm text-zinc-900 dark:text-zinc-100 placeholder-zinc-400 dark:placeholder-zinc-600 focus:outline-none focus:border-green-500"
              />
            </div>
          ))}

          {error && (
            <p className="text-red-600 dark:text-red-400 text-xs bg-red-50 dark:bg-red-950/40 border border-red-200 dark:border-red-900 rounded px-3 py-2">
              {error}
            </p>
          )}

          <div className="flex gap-3 pt-2">
            <button
              type="submit"
              disabled={loading}
              className="flex-1 bg-green-600 hover:bg-green-500 dark:bg-green-700 dark:hover:bg-green-600 disabled:opacity-50 text-white text-sm font-medium py-2 rounded transition-colors"
            >
              {loading ? 'Running…' : 'Run'}
            </button>
            <button
              type="button"
              onClick={onClose}
              className="flex-1 bg-zinc-100 dark:bg-zinc-700 hover:bg-zinc-200 dark:hover:bg-zinc-600 text-zinc-700 dark:text-zinc-200 text-sm py-2 rounded transition-colors"
            >
              Cancel
            </button>
          </div>
        </form>
      </div>
    </div>
  )
}
