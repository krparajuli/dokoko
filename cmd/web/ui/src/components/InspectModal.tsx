interface Props {
  title: string
  data: unknown
  onClose: () => void
}

export default function InspectModal({ title, data, onClose }: Props) {
  return (
    <div
      className="fixed inset-0 z-50 flex items-center justify-center bg-black/60 dark:bg-black/70"
      onClick={(e) => e.target === e.currentTarget && onClose()}
    >
      <div className="bg-white dark:bg-zinc-900 border border-zinc-200 dark:border-zinc-700 rounded-lg w-full max-w-2xl max-h-[80vh] flex flex-col shadow-lg">
        <div className="flex items-center justify-between px-5 py-3 border-b border-zinc-200 dark:border-zinc-800">
          <h3 className="text-green-600 dark:text-green-400 font-bold">{title}</h3>
          <button onClick={onClose} className="text-zinc-400 dark:text-zinc-500 hover:text-zinc-800 dark:hover:text-zinc-200 text-xl leading-none">×</button>
        </div>
        <pre className="overflow-auto p-5 text-xs text-zinc-700 dark:text-zinc-300 leading-relaxed whitespace-pre-wrap break-words">
          {JSON.stringify(data, null, 2)}
        </pre>
      </div>
    </div>
  )
}
