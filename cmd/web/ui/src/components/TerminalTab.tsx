import { useState, useEffect, useCallback, useRef } from 'react'
import { listWebCatalog, provisionWeb, getWebSession, terminateWeb, scanPorts, getPortMappings, removePortMappings } from '../api.ts'
import type { CatalogEntry, MappedPort, WebSession, PortScanResult } from '../types.ts'

// ── Port label helpers ────────────────────────────────────────────────────────

const PROCESS_LABELS: Record<string, string> = {
  python3: 'Python',
  python: 'Python',
  node: 'Node.js',
  nodejs: 'Node.js',
  nginx: 'Nginx',
  apache2: 'Apache',
  httpd: 'Apache',
  ruby: 'Ruby',
  java: 'Java',
  go: 'Go',
  deno: 'Deno',
  bun: 'Bun',
  uvicorn: 'Python (Uvicorn)',
  gunicorn: 'Python (Gunicorn)',
  php: 'PHP',
  caddy: 'Caddy',
  redis: 'Redis',
  postgres: 'PostgreSQL',
  postgresql: 'PostgreSQL',
  mysql: 'MySQL',
  mongod: 'MongoDB',
  jupyter: 'Jupyter',
}

function friendlyPortLabel(p: MappedPort): string {
  if (!p.process) return 'Service'
  const key = p.process.toLowerCase()
  if (PROCESS_LABELS[key]) return PROCESS_LABELS[key]
  // Capitalise first letter for unknown processes
  return p.process.charAt(0).toUpperCase() + p.process.slice(1)
}

// ── User-ID helpers ───────────────────────────────────────────────────────────

const USER_ID_KEY = 'dokoko_terminal_user_id'

function getOrCreateUserID(): string {
  let id = localStorage.getItem(USER_ID_KEY)
  if (!id) {
    id = 'user-' + Math.random().toString(36).slice(2, 10)
    localStorage.setItem(USER_ID_KEY, id)
  }
  return id
}

// ── Main component ────────────────────────────────────────────────────────────

export default function TerminalTab() {
  const userID = useRef(getOrCreateUserID()).current

  const [catalog, setCatalog]     = useState<CatalogEntry[]>([])
  const [session, setSession]     = useState<WebSession | null>(null)
  const [loading, setLoading]     = useState(true)
  const [working, setWorking]     = useState(false)
  const [toast, setToast]         = useState('')
  const [ttydReady, setTtydReady]         = useState(false)
  const [portScan, setPortScan]           = useState<PortScanResult | null>(null)
  const [scanning, setScanning]           = useState(false)
  const pollRef                           = useRef<number | null>(null)
  const ttydPollRef                       = useRef<number | null>(null)
  const portScanIntervalRef               = useRef<number | null>(null)

  // ── Helpers ─────────────────────────────────────────────────────────────────

  const notify = (msg: string) => {
    setToast(msg)
    setTimeout(() => setToast(''), 3_000)
  }

  const stopPoll = () => {
    if (pollRef.current !== null) {
      clearInterval(pollRef.current)
      pollRef.current = null
    }
  }

  const stopTtydPoll = () => {
    if (ttydPollRef.current !== null) {
      clearInterval(ttydPollRef.current)
      ttydPollRef.current = null
    }
  }

  const stopPortScanInterval = () => {
    if (portScanIntervalRef.current !== null) {
      clearInterval(portScanIntervalRef.current)
      portScanIntervalRef.current = null
    }
  }

  const startPoll = useCallback(() => {
    stopPoll()
    pollRef.current = window.setInterval(async () => {
      try {
        const sess = await getWebSession(userID) as WebSession
        setSession(sess)
        if (sess.status === 'ready' || sess.status === 'error' || sess.status === 'stopped') {
          stopPoll()
        }
      } catch {
        stopPoll()
        setSession(null)
      }
    }, 2_000)
  }, [userID])

  // ── Poll ttyd endpoint until it responds (ttyd starts after Docker "ready") ──

  useEffect(() => {
    if (!session || session.status !== 'ready' || !session.terminal_path || ttydReady) {
      return
    }

    const path = session.terminal_path

    const check = async () => {
      try {
        const res = await fetch(path, { method: 'GET' })
        if (res.ok) {
          setTtydReady(true)
          stopTtydPoll()
        }
      } catch {
        // ttyd not up yet — keep polling
      }
    }

    check() // immediate first attempt
    ttydPollRef.current = window.setInterval(check, 3_000)

    return stopTtydPoll
  // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [session?.status, session?.terminal_path, session?.container_id, ttydReady])

  // Reset ttydReady and portScan whenever the container changes.
  useEffect(() => {
    setTtydReady(false)
    setPortScan(null)
    stopPortScanInterval()
  // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [session?.container_id])

  // Auto-scan ports every 2 minutes while the terminal is ready.
  useEffect(() => {
    if (!ttydReady || !session?.status || session.status !== 'ready') {
      stopPortScanInterval()
      return
    }

    const runScan = async () => {
      try {
        await scanPorts(userID)
        const result = await getPortMappings(userID) as PortScanResult
        setPortScan(result)
      } catch {
        // silent — don't toast on background scans
      }
    }

    portScanIntervalRef.current = window.setInterval(runScan, 2 * 60 * 1000)
    return stopPortScanInterval
  // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [ttydReady, session?.status, userID])

  // ── Load catalog + check existing session on mount ────────────────────────

  useEffect(() => {
    let cancelled = false

    async function init() {
      try {
        const [cat, sess] = await Promise.allSettled([
          listWebCatalog() as Promise<CatalogEntry[]>,
          getWebSession(userID) as Promise<WebSession>,
        ])
        if (cancelled) return

        if (cat.status === 'fulfilled') setCatalog(cat.value ?? [])

        if (sess.status === 'fulfilled') {
          setSession(sess.value)
          if (sess.value.status === 'provisioning') startPoll()
        }
      } finally {
        if (!cancelled) setLoading(false)
      }
    }

    init()
    return () => { cancelled = true; stopPoll(); stopTtydPoll(); stopPortScanInterval() }
  }, [userID, startPoll])

  // ── Actions ──────────────────────────────────────────────────────────────────

  const handleProvision = async (catalogID: string) => {
    setWorking(true)
    try {
      const sess = await provisionWeb(userID, catalogID) as WebSession
      setSession(sess)
      if (sess.status === 'provisioning') startPoll()
    } catch (e: unknown) {
      notify('Provision failed: ' + (e instanceof Error ? e.message : String(e)))
    } finally {
      setWorking(false)
    }
  }

  const handleTerminate = async () => {
    if (!session) return
    setWorking(true)
    try {
      await terminateWeb(userID)
      setSession(null)
      setTtydReady(false)
      setPortScan(null)
      stopPortScanInterval()
      notify('Session terminated')
    } catch (e: unknown) {
      notify('Terminate failed: ' + (e instanceof Error ? e.message : String(e)))
    } finally {
      setWorking(false)
    }
  }

  const handleScanPorts = async () => {
    setScanning(true)
    try {
      await scanPorts(userID)
      const result = await getPortMappings(userID) as PortScanResult
      setPortScan(result)
      notify('Port scan complete')
    } catch (e: unknown) {
      notify('Scan failed: ' + (e instanceof Error ? e.message : String(e)))
    } finally {
      setScanning(false)
    }
  }

  const handleUnmapPorts = async () => {
    setScanning(true)
    try {
      await removePortMappings(userID)
      setPortScan(null)
      notify('Ports unmapped')
    } catch (e: unknown) {
      notify('Unmap failed: ' + (e instanceof Error ? e.message : String(e)))
    } finally {
      setScanning(false)
    }
  }

  // ── Render ────────────────────────────────────────────────────────────────────

  if (loading) {
    return <p className="text-zinc-400 dark:text-zinc-600 text-sm">Loading…</p>
  }

  return (
    <div className="space-y-4">
      {/* Toast */}
      {toast && (
        <p className="text-green-600 dark:text-green-400 text-xs">{toast}</p>
      )}

      {/* No session — show image selector */}
      {!session && (
        <div className="space-y-4">
          <div>
            <h2 className="text-sm font-semibold text-zinc-700 dark:text-zinc-200 mb-1">
              Start a Terminal Session
            </h2>
            <p className="text-xs text-zinc-500 dark:text-zinc-400">
              Select an image to spin up your personal container with an interactive shell.
            </p>
          </div>
          <div className="grid grid-cols-1 sm:grid-cols-2 lg:grid-cols-3 gap-3">
            {catalog.map((entry) => (
              <button
                key={entry.id}
                disabled={working}
                onClick={() => handleProvision(entry.id)}
                className="text-left p-4 rounded border border-zinc-200 dark:border-zinc-700
                           bg-white dark:bg-zinc-900 hover:border-green-400 dark:hover:border-green-600
                           hover:bg-zinc-50 dark:hover:bg-zinc-800 transition-colors
                           disabled:opacity-50 disabled:cursor-not-allowed"
              >
                <div className="font-medium text-sm text-zinc-800 dark:text-zinc-100 mb-1">
                  {entry.display_name}
                </div>
                <div className="text-xs text-zinc-500 dark:text-zinc-400 mb-2">
                  {entry.description}
                </div>
                <code className="text-xs text-cyan-600 dark:text-cyan-400">{entry.image}</code>
              </button>
            ))}
          </div>
          {working && (
            <p className="text-zinc-500 dark:text-zinc-400 text-xs">Provisioning container…</p>
          )}
        </div>
      )}

      {/* Session provisioning / terminating */}
      {session && (session.status === 'provisioning' || session.status === 'terminating') && (
        <div className="space-y-3">
          <div className="flex items-center gap-3 text-sm text-zinc-600 dark:text-zinc-300">
            <Spinner />
            <span>
              {session.status === 'provisioning'
                ? `Provisioning container (${session.catalog_id})…`
                : 'Terminating session…'}
            </span>
          </div>
          <p className="text-xs text-zinc-400 dark:text-zinc-500">
            Installing ttyd + tmux and starting the terminal. This may take a minute on first pull.
          </p>
        </div>
      )}

      {/* Session error */}
      {session && session.status === 'error' && (
        <div className="space-y-3">
          <div className="p-3 rounded border border-red-300 dark:border-red-700 bg-red-50 dark:bg-red-950/30 text-xs text-red-700 dark:text-red-300">
            Provision failed: {session.error || 'unknown error'}
          </div>
          <button
            onClick={handleTerminate}
            className="px-3 py-1.5 rounded text-xs font-medium bg-zinc-100 dark:bg-zinc-800 hover:bg-zinc-200 dark:hover:bg-zinc-700 text-zinc-800 dark:text-zinc-200"
          >
            ↺ Clear &amp; start over
          </button>
        </div>
      )}

      {/* Session ready — wait for ttyd, then embed iframe */}
      {session && session.status === 'ready' && session.terminal_path && (
        <div className="space-y-2">
          <div className="flex items-center justify-between flex-wrap gap-2">
            <div className="text-xs text-zinc-500 dark:text-zinc-400">
              Container: <code className="text-yellow-600 dark:text-yellow-400">{session.container_name}</code>
              <span className="ml-2 text-zinc-400">·</span>
              <span className="ml-2">{session.catalog_id}</span>
            </div>
            <div className="flex gap-2">
              {ttydReady && (
                <a
                  href={session.terminal_path}
                  target="_blank"
                  rel="noopener noreferrer"
                  className="px-3 py-1.5 rounded text-xs font-medium bg-zinc-100 dark:bg-zinc-800 hover:bg-zinc-200 dark:hover:bg-zinc-700 text-zinc-800 dark:text-zinc-200"
                >
                  ↗ Open in new tab
                </a>
              )}
              <button
                disabled={working}
                onClick={handleTerminate}
                className="px-3 py-1.5 rounded text-xs font-medium bg-red-100 dark:bg-red-900/30 hover:bg-red-200 dark:hover:bg-red-900/60 text-red-700 dark:text-red-300 disabled:opacity-50"
              >
                Terminate
              </button>
            </div>
          </div>

          {/* ttyd still starting */}
          {!ttydReady && (
            <div className="flex items-center gap-3 text-sm text-zinc-600 dark:text-zinc-300 py-6">
              <Spinner />
              <span className="text-xs">Starting terminal… checking every 3 s</span>
            </div>
          )}

          {/* ttyd ready — show iframe */}
          {ttydReady && (
            <div className="rounded border border-zinc-200 dark:border-zinc-700 overflow-hidden"
                 style={{ height: 'calc(100vh - 260px)', minHeight: 400 }}>
              <iframe
                src={session.terminal_path}
                className="w-full h-full border-0"
                title="Web Terminal"
                allow="clipboard-read; clipboard-write"
              />
            </div>
          )}

          {/* Port scanning */}
          {ttydReady && (
            <div className="pt-2 border-t border-zinc-200 dark:border-zinc-700 space-y-2">
              <div className="flex items-center gap-2">
                <button
                  disabled={scanning || working}
                  onClick={handleScanPorts}
                  className="px-3 py-1.5 rounded text-xs font-medium bg-zinc-100 dark:bg-zinc-800 hover:bg-zinc-200 dark:hover:bg-zinc-700 text-zinc-800 dark:text-zinc-200 disabled:opacity-50 disabled:cursor-not-allowed"
                >
                  {scanning ? 'Scanning…' : '⚡ Scan Ports'}
                </button>
                {portScan && portScan.ports.length > 0 && (
                  <button
                    disabled={scanning || working}
                    onClick={handleUnmapPorts}
                    className="px-3 py-1.5 rounded text-xs font-medium bg-zinc-100 dark:bg-zinc-800 hover:bg-zinc-200 dark:hover:bg-zinc-700 text-zinc-500 dark:text-zinc-400 disabled:opacity-50 disabled:cursor-not-allowed"
                  >
                    Unmap
                  </button>
                )}
              </div>
              {portScan && portScan.ports.length > 0 && (
                <div className="space-y-1.5">
                  {[...portScan.ports].sort((a, b) => a.container_port - b.container_port).map((p) => (
                    <div key={p.container_port} className="flex items-center justify-between gap-3 text-xs">
                      <div className="flex flex-col min-w-0">
                        <span className="font-medium text-zinc-700 dark:text-zinc-200">
                          {friendlyPortLabel(p)}
                        </span>
                        <span className="text-zinc-400 dark:text-zinc-500 font-mono text-[10px]">
                          port {p.container_port}
                        </span>
                      </div>
                      <a
                        href={p.url}
                        target="_blank"
                        rel="noopener noreferrer"
                        className="shrink-0 px-2 py-0.5 rounded bg-cyan-50 dark:bg-cyan-900/30 text-cyan-700 dark:text-cyan-300 hover:bg-cyan-100 dark:hover:bg-cyan-900/50 font-medium"
                      >
                        Open ↗
                      </a>
                    </div>
                  ))}
                </div>
              )}
              {portScan && portScan.ports.length === 0 && (
                <p className="text-xs text-zinc-400 dark:text-zinc-500">No listening ports found.</p>
              )}
            </div>
          )}
        </div>
      )}
    </div>
  )
}

// ── Spinner ───────────────────────────────────────────────────────────────────

function Spinner() {
  return (
    <span
      className="inline-block w-4 h-4 border-2 border-zinc-300 dark:border-zinc-600 border-t-green-500 dark:border-t-green-400 rounded-full animate-spin"
      aria-hidden="true"
    />
  )
}
