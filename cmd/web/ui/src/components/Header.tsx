import type { Tab, HealthStatus } from '../types.ts'
import { useTheme } from '../context/ThemeContext.tsx'

const TABS: { id: Tab; label: string }[] = [
  { id: 'images',     label: 'Images' },
  { id: 'containers', label: 'Containers' },
  { id: 'volumes',    label: 'Volumes' },
  { id: 'networks',   label: 'Networks' },
  { id: 'execs',      label: 'Execs' },
  { id: 'terminal',   label: 'Terminal' },
]

interface Props {
  activeTab: Tab
  onTabChange: (tab: Tab) => void
  dockerStatus: HealthStatus
}

export default function Header({ activeTab, onTabChange, dockerStatus }: Props) {
  const { theme, toggle } = useTheme()

  return (
    <header className="bg-zinc-50 dark:bg-zinc-900 border-b border-zinc-200 dark:border-zinc-800 flex-shrink-0">
      <div className="flex items-center justify-between px-4 py-2 border-b border-zinc-200 dark:border-zinc-800">
        <div className="flex items-center gap-3">
          <span className="text-green-600 dark:text-green-400 font-bold text-lg tracking-tight">dokoko</span>
          <span className="text-zinc-400 dark:text-zinc-600 text-sm">docker manager</span>
        </div>
        <div className="flex items-center gap-4 text-xs">
          <div className="flex items-center gap-2">
            <span
              className={`w-2 h-2 rounded-full ${dockerStatus.docker ? 'bg-green-500' : 'bg-red-500'}`}
            />
            <span className={dockerStatus.docker ? 'text-green-600 dark:text-green-400' : 'text-red-600 dark:text-red-400'}>
              {dockerStatus.docker ? 'Docker connected' : (dockerStatus.error ?? 'Docker disconnected')}
            </span>
          </div>
          <button
            onClick={toggle}
            title={theme === 'dark' ? 'Switch to light mode' : 'Switch to dark mode'}
            className="text-zinc-500 hover:text-zinc-800 dark:hover:text-zinc-200 transition-colors select-none"
          >
            {theme === 'dark' ? '☀' : '☾'}
          </button>
        </div>
      </div>

      <nav className="flex px-4">
        {TABS.map((tab) => (
          <button
            key={tab.id}
            onClick={() => onTabChange(tab.id)}
            className={`px-4 py-2 text-sm border-b-2 transition-colors ${
              activeTab === tab.id
                ? 'border-green-500 dark:border-green-400 text-green-600 dark:text-green-400'
                : 'border-transparent text-zinc-500 dark:text-zinc-400 hover:text-zinc-800 dark:hover:text-zinc-200 hover:border-zinc-400 dark:hover:border-zinc-600'
            }`}
          >
            {tab.label}
          </button>
        ))}
      </nav>
    </header>
  )
}
