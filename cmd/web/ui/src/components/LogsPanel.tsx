import { useEffect, useRef, useState } from 'react'

const MAX_LINES = 500

export default function LogsPanel() {
  const [lines, setLines] = useState<string[]>([])
  const [open, setOpen] = useState(true)
  const [autoScroll, setAutoScroll] = useState(true)
  const bottomRef = useRef<HTMLDivElement>(null)

  useEffect(() => {
    const es = new EventSource('/api/logs/stream')
    es.onmessage = (e: MessageEvent<string>) => {
      setLines((prev) => {
        const next = [...prev, e.data]
        return next.length > MAX_LINES ? next.slice(next.length - MAX_LINES) : next
      })
    }
    return () => es.close()
  }, [])

  useEffect(() => {
    if (autoScroll) {
      bottomRef.current?.scrollIntoView({ behavior: 'smooth' })
    }
  }, [lines, autoScroll])

  const colorLine = (line: string) => {
    if (line.includes('[ERROR]')) return 'text-red-400'
    if (line.includes('[WARN]'))  return 'text-yellow-400'
    if (line.includes('[INFO]'))  return 'text-zinc-300'
    if (line.includes('[DEBUG]')) return 'text-zinc-500'
    return 'text-zinc-400'
  }

  return (
    <div className="flex-shrink-0 bg-zinc-900 border-t border-zinc-800">
      <div className="flex items-center justify-between px-4 py-1 border-b border-zinc-800">
        <div className="flex items-center gap-3">
          <button
            onClick={() => setOpen((v) => !v)}
            className="text-xs text-zinc-400 hover:text-zinc-200 font-medium"
          >
            {open ? '▾' : '▸'} Logs
          </button>
          <span className="text-zinc-600 text-xs">{lines.length} lines</span>
        </div>
        {open && (
          <div className="flex items-center gap-3">
            <label className="flex items-center gap-1 text-xs text-zinc-500 cursor-pointer select-none">
              <input
                type="checkbox"
                checked={autoScroll}
                onChange={(e) => setAutoScroll(e.target.checked)}
                className="accent-green-500"
              />
              auto-scroll
            </label>
            <button
              onClick={() => setLines([])}
              className="text-xs text-zinc-500 hover:text-zinc-300"
            >
              clear
            </button>
          </div>
        )}
      </div>

      {open && (
        <div className="h-48 overflow-y-auto px-4 py-2 text-xs leading-relaxed">
          {lines.length === 0 ? (
            <p className="text-zinc-600 italic">No log output yet…</p>
          ) : (
            lines.map((line, i) => (
              <div key={i} className={colorLine(line)}>
                {line}
              </div>
            ))
          )}
          <div ref={bottomRef} />
        </div>
      )}
    </div>
  )
}
