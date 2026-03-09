import { useEffect, useCallback, useRef, useState } from 'react'
import { listWebCatalog, provisionWeb, getWebSession, terminateWeb, scanPorts, getPortMappings, removePortMappings, getContainerEnv, setContainerEnv, getImageVars } from '../api.ts'
import { useAuth } from '../context/AuthContext.tsx'
import type { CatalogEntry, MappedPort, WebSession, PortScanResult, VarDef } from '../types.ts'

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

// ── Main component ────────────────────────────────────────────────────────────

export default function TerminalTab() {
  const { user } = useAuth()
  const userID = user!.username

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

  // ── Env var state ────────────────────────────────────────────────────────────
  const [envVars, setEnvVars]           = useState<Record<string, string>>({})
  const [loadingEnv, setLoadingEnv]     = useState(false)
  const [savingEnv, setSavingEnv]       = useState(false)
  const [editingKey, setEditingKey]     = useState<string | null>(null) // key being edited; null=none
  const [draftKey, setDraftKey]         = useState('')
  const [draftValue, setDraftValue]     = useState('')
  const [addRows, setAddRows]           = useState<{key: string; value: string}[]>([])

  // ── Image var schema state ───────────────────────────────────────────────────
  const [pendingImage, setPendingImage]     = useState<string | null>(null)
  const [pendingSchema, setPendingSchema]   = useState<VarDef[]>([])
  const [pendingEnvForm, setPendingEnvForm] = useState<Record<string, string>>({})
  const [loadingSchema, setLoadingSchema]   = useState(false)
  const [sessionSchema, setSessionSchema]   = useState<VarDef[]>([])

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

  // ── Load env vars on mount ───────────────────────────────────────────────────

  useEffect(() => {
    let cancelled = false
    setLoadingEnv(true)
    getContainerEnv(userID)
      .then(vars => { if (!cancelled) setEnvVars(vars ?? {}) })
      .catch(() => {})
      .finally(() => { if (!cancelled) setLoadingEnv(false) })
    return () => { cancelled = true }
  }, [userID])

  // ── Load schema for active session's catalog image ───────────────────────────

  useEffect(() => {
    if (!session?.catalog_id) {
      setSessionSchema([])
      return
    }
    let cancelled = false
    getImageVars(session.catalog_id)
      .then(({ vars }) => { if (!cancelled) setSessionSchema(vars ?? []) })
      .catch(() => { if (!cancelled) setSessionSchema([]) })
    return () => { cancelled = true }
  }, [session?.catalog_id])

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

  const handleImageClick = async (catalogID: string) => {
    setLoadingSchema(true)
    try {
      const { vars } = await getImageVars(catalogID)
      if (vars && vars.length > 0) {
        // Pre-fill form with existing env vars or schema defaults
        const form: Record<string, string> = {}
        for (const v of vars) {
          form[v.name] = envVars[v.name] ?? (v.has_default ? v.default_value : '')
        }
        setPendingImage(catalogID)
        setPendingSchema(vars)
        setPendingEnvForm(form)
      } else {
        // No schema — provision immediately
        await handleProvision(catalogID)
      }
    } catch {
      // Schema fetch failed — provision anyway
      await handleProvision(catalogID)
    } finally {
      setLoadingSchema(false)
    }
  }

  const handleLaunch = async () => {
    if (!pendingImage) return
    setWorking(true)
    try {
      // Persist non-empty form values into env store before provisioning
      const nonEmpty = Object.fromEntries(
        Object.entries(pendingEnvForm).filter(([, v]) => v !== '')
      )
      if (Object.keys(nonEmpty).length > 0) {
        const updated = { ...envVars, ...nonEmpty }
        const result = await setContainerEnv(userID, updated) as Record<string, string>
        setEnvVars(result ?? updated)
      }
      const imageID = pendingImage
      setPendingImage(null)
      setPendingSchema([])
      setPendingEnvForm({})
      const sess = await provisionWeb(userID, imageID) as WebSession
      setSession(sess)
      if (sess.status === 'provisioning') startPoll()
    } catch (e: unknown) {
      notify('Launch failed: ' + (e instanceof Error ? e.message : String(e)))
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

  // ── Env var handlers ─────────────────────────────────────────────────────────

  const handleSaveEnvVar = async () => {
    if (!draftKey.trim()) return
    setSavingEnv(true)
    try {
      const updated = { ...envVars }
      if (editingKey !== draftKey) delete updated[editingKey!]
      updated[draftKey] = draftValue
      const result = await setContainerEnv(userID, updated) as Record<string, string>
      setEnvVars(result ?? updated)
      setEditingKey(null); setDraftKey(''); setDraftValue('')
      notify('Variable saved')
    } catch (e: unknown) {
      notify('Save failed: ' + (e instanceof Error ? e.message : String(e)))
    } finally {
      setSavingEnv(false)
    }
  }

  const handleSaveAddRows = async () => {
    const valid = addRows.filter(r => r.key.trim())
    if (valid.length === 0) return
    setSavingEnv(true)
    try {
      const updated = { ...envVars }
      for (const r of valid) updated[r.key] = r.value
      const result = await setContainerEnv(userID, updated) as Record<string, string>
      setEnvVars(result ?? updated)
      setAddRows([])
      notify(`${valid.length} variable${valid.length > 1 ? 's' : ''} saved`)
    } catch (e: unknown) {
      notify('Save failed: ' + (e instanceof Error ? e.message : String(e)))
    } finally {
      setSavingEnv(false)
    }
  }

  const handleDeleteEnvVar = async (key: string) => {
    setSavingEnv(true)
    try {
      const updated = { ...envVars }
      delete updated[key]
      const result = await setContainerEnv(userID, updated) as Record<string, string>
      setEnvVars(result ?? updated)
      notify('Variable removed')
    } catch (e: unknown) {
      notify('Remove failed: ' + (e instanceof Error ? e.message : String(e)))
    } finally {
      setSavingEnv(false)
    }
  }

  const handleEditEnvVar = (key: string) => {
    setEditingKey(key); setDraftKey(key); setDraftValue(envVars[key])
  }

  const handleCancelEdit = () => {
    setEditingKey(null); setDraftKey(''); setDraftValue('')
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

      {/* No session — show image selector or configure panel */}
      {!session && !pendingImage && (
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
                disabled={working || loadingSchema}
                onClick={() => handleImageClick(entry.id)}
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
          {(working || loadingSchema) && (
            <p className="text-zinc-500 dark:text-zinc-400 text-xs">
              {loadingSchema ? 'Loading configuration…' : 'Provisioning container…'}
            </p>
          )}
        </div>
      )}

      {/* Configure panel — shown when selected image has a var schema */}
      {!session && pendingImage && (
        <div className="space-y-4">
          <div className="flex items-center justify-between">
            <div>
              <h2 className="text-sm font-semibold text-zinc-700 dark:text-zinc-200 mb-1">
                Configure {catalog.find(e => e.id === pendingImage)?.display_name ?? pendingImage}
              </h2>
              <p className="text-xs text-zinc-500 dark:text-zinc-400">
                Set the environment variables required by this image before launching.
              </p>
            </div>
            <button
              onClick={() => { setPendingImage(null); setPendingSchema([]); setPendingEnvForm({}) }}
              disabled={working}
              className="text-xs text-zinc-400 hover:text-zinc-600 dark:hover:text-zinc-300 disabled:opacity-50"
            >
              ← Back
            </button>
          </div>

          <div className="border border-zinc-200 dark:border-zinc-700 rounded divide-y divide-zinc-100 dark:divide-zinc-800">
            {pendingSchema.map((v) => (
              <div key={v.name} className="flex flex-col gap-1 p-3">
                <div className="flex items-center gap-2">
                  <code className="text-xs font-mono text-amber-600 dark:text-amber-400">{v.name}</code>
                  {v.required
                    ? <span className="text-[10px] font-medium text-red-600 dark:text-red-400 bg-red-50 dark:bg-red-950/40 px-1.5 py-0.5 rounded">required</span>
                    : <span className="text-[10px] font-medium text-zinc-400 dark:text-zinc-500 bg-zinc-100 dark:bg-zinc-800 px-1.5 py-0.5 rounded">optional</span>
                  }
                  {v.has_default && !pendingEnvForm[v.name] && (
                    <span className="text-[10px] text-zinc-400 dark:text-zinc-500">default: {v.default_value}</span>
                  )}
                </div>
                <input
                  type={v.name.toLowerCase().includes('key') || v.name.toLowerCase().includes('secret') || v.name.toLowerCase().includes('password') ? 'password' : 'text'}
                  value={pendingEnvForm[v.name] ?? ''}
                  onChange={e => setPendingEnvForm(f => ({ ...f, [v.name]: e.target.value }))}
                  placeholder={v.has_default ? v.default_value : v.required ? 'Required' : 'Optional'}
                  className="text-xs font-mono px-2 py-1 rounded border border-zinc-300 dark:border-zinc-600 bg-white dark:bg-zinc-900 text-zinc-700 dark:text-zinc-300 focus:outline-none focus:border-zinc-500 placeholder:text-zinc-300 dark:placeholder:text-zinc-600"
                />
              </div>
            ))}
          </div>

          <div className="flex items-center gap-3">
            <button
              onClick={handleLaunch}
              disabled={working || pendingSchema.some(v => v.required && !pendingEnvForm[v.name]?.trim() && !v.has_default)}
              className="px-4 py-1.5 rounded text-xs font-medium bg-green-600 hover:bg-green-700 text-white disabled:opacity-50 disabled:cursor-not-allowed"
            >
              {working ? 'Launching…' : '▶ Launch'}
            </button>
            {pendingSchema.some(v => v.required && !pendingEnvForm[v.name]?.trim() && !v.has_default) && (
              <span className="text-xs text-red-500 dark:text-red-400">
                Fill in all required variables to continue.
              </span>
            )}
          </div>
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

      {/* Environment Variables — always visible */}
      {!loading && (
        <div className="border border-zinc-200 dark:border-zinc-700 rounded space-y-2 p-3">
          <div className="flex items-center justify-between">
            <h3 className="text-xs font-semibold text-zinc-700 dark:text-zinc-200">
              Environment Variables
            </h3>
            {session && session.status === 'ready'
              ? <span className="text-[10px] text-green-600 dark:text-green-400">live in container</span>
              : <span className="text-[10px] text-zinc-400">applied on next start</span>
            }
          </div>

          {loadingEnv ? (
            <p className="text-xs text-zinc-400">Loading…</p>
          ) : (
            <div className="space-y-1">
              {Object.entries(envVars).map(([key, value]) =>
                editingKey === key ? (
                  <EnvVarForm
                    key={key}
                    draftKey={draftKey}
                    draftValue={draftValue}
                    saving={savingEnv}
                    onKeyChange={setDraftKey}
                    onValueChange={setDraftValue}
                    onSave={handleSaveEnvVar}
                    onCancel={handleCancelEdit}
                  />
                ) : (
                  <div key={key} className="flex items-center gap-2 text-xs group py-0.5">
                    <code className="w-36 shrink-0 font-mono text-amber-600 dark:text-amber-400 truncate">{key}</code>
                    {sessionSchema.find(v => v.name === key) && (() => {
                      const vd = sessionSchema.find(v => v.name === key)!
                      return vd.required
                        ? <span className="shrink-0 text-[9px] font-medium text-red-500 dark:text-red-400 bg-red-50 dark:bg-red-950/40 px-1 py-0.5 rounded leading-none">req</span>
                        : <span className="shrink-0 text-[9px] font-medium text-zinc-400 dark:text-zinc-500 bg-zinc-100 dark:bg-zinc-800 px-1 py-0.5 rounded leading-none">opt</span>
                    })()}
                    <code className="flex-1 font-mono text-zinc-500 dark:text-zinc-400 truncate">{value || '""'}</code>
                    <div className="flex gap-1 opacity-0 group-hover:opacity-100 transition-opacity shrink-0">
                      <button
                        onClick={() => handleEditEnvVar(key)}
                        className="px-1 text-zinc-400 hover:text-zinc-700 dark:hover:text-zinc-200"
                        title="Edit"
                      >✎</button>
                      <button
                        onClick={() => handleDeleteEnvVar(key)}
                        disabled={savingEnv}
                        className="px-1 text-zinc-400 hover:text-red-500 disabled:opacity-50"
                        title="Remove"
                      >✕</button>
                    </div>
                  </div>
                )
              )}

              {addRows.length > 0 ? (
                <div className="space-y-1 pt-1 border-t border-zinc-100 dark:border-zinc-800 mt-1">
                  {addRows.map((row, i) => (
                    <div key={i} className="flex items-center gap-1.5">
                      <input
                        value={row.key}
                        onChange={e => setAddRows(rs => rs.map((r, j) => j === i ? { ...r, key: e.target.value } : r))}
                        placeholder="KEY"
                        // eslint-disable-next-line jsx-a11y/no-autofocus
                        autoFocus={i === 0 && addRows.length === 1}
                        className="w-36 shrink-0 text-xs font-mono px-1.5 py-0.5 rounded border border-zinc-300 dark:border-zinc-600 bg-white dark:bg-zinc-800 text-amber-600 dark:text-amber-400 focus:outline-none focus:border-zinc-500"
                      />
                      <input
                        value={row.value}
                        onChange={e => setAddRows(rs => rs.map((r, j) => j === i ? { ...r, value: e.target.value } : r))}
                        onKeyDown={e => { if (e.key === 'Enter') handleSaveAddRows() }}
                        placeholder="value"
                        className="flex-1 text-xs font-mono px-1.5 py-0.5 rounded border border-zinc-300 dark:border-zinc-600 bg-white dark:bg-zinc-800 text-zinc-500 dark:text-zinc-400 focus:outline-none focus:border-zinc-500"
                      />
                      <button
                        onClick={() => setAddRows(rs => rs.filter((_, j) => j !== i))}
                        disabled={savingEnv}
                        className="shrink-0 px-1 text-zinc-400 hover:text-red-500 disabled:opacity-50"
                        title="Remove row"
                      >✕</button>
                    </div>
                  ))}
                  <div className="flex items-center gap-2 pt-0.5">
                    <button
                      onClick={() => setAddRows(rs => [...rs, { key: '', value: '' }])}
                      disabled={savingEnv}
                      className="text-xs text-zinc-400 hover:text-zinc-600 dark:hover:text-zinc-300 disabled:opacity-50"
                    >+ Row</button>
                    <button
                      onClick={handleSaveAddRows}
                      disabled={savingEnv || !addRows.some(r => r.key.trim())}
                      className="text-xs px-2 py-0.5 rounded bg-zinc-800 dark:bg-zinc-200 text-white dark:text-zinc-900 disabled:opacity-50"
                    >{savingEnv ? 'Saving…' : 'Save All'}</button>
                    <button
                      onClick={() => setAddRows([])}
                      disabled={savingEnv}
                      className="text-xs text-zinc-400 hover:text-zinc-600 dark:hover:text-zinc-300 disabled:opacity-50"
                    >Cancel</button>
                  </div>
                </div>
              ) : (
                <button
                  onClick={() => setAddRows([{ key: '', value: '' }])}
                  disabled={savingEnv}
                  className="text-xs text-zinc-400 dark:text-zinc-500 hover:text-zinc-600 dark:hover:text-zinc-300 disabled:opacity-50 mt-1"
                >
                  + Add Variable
                </button>
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

// ── EnvVarForm ────────────────────────────────────────────────────────────────

function EnvVarForm({ draftKey, draftValue, saving, onKeyChange, onValueChange, onSave, onCancel }: {
  draftKey: string
  draftValue: string
  saving: boolean
  onKeyChange: (v: string) => void
  onValueChange: (v: string) => void
  onSave: () => void
  onCancel: () => void
}) {
  return (
    <div className="flex items-center gap-1.5 py-0.5">
      <input
        value={draftKey}
        onChange={e => onKeyChange(e.target.value)}
        placeholder="KEY"
        className="w-36 shrink-0 text-xs font-mono px-1.5 py-0.5 rounded border border-zinc-300 dark:border-zinc-600 bg-white dark:bg-zinc-800 text-amber-600 dark:text-amber-400 focus:outline-none focus:border-zinc-500"
        autoFocus
      />
      <input
        value={draftValue}
        onChange={e => onValueChange(e.target.value)}
        onKeyDown={e => { if (e.key === 'Enter') onSave() }}
        placeholder="value"
        className="flex-1 text-xs font-mono px-1.5 py-0.5 rounded border border-zinc-300 dark:border-zinc-600 bg-white dark:bg-zinc-800 text-zinc-500 dark:text-zinc-400 focus:outline-none focus:border-zinc-500"
      />
      <button
        onClick={onSave}
        disabled={saving || !draftKey.trim()}
        className="shrink-0 text-xs px-2 py-0.5 rounded bg-zinc-800 dark:bg-zinc-200 text-white dark:text-zinc-900 disabled:opacity-50"
      >
        Save
      </button>
      <button
        onClick={onCancel}
        disabled={saving}
        className="shrink-0 text-xs text-zinc-400 hover:text-zinc-600 dark:hover:text-zinc-300 disabled:opacity-50"
      >
        Cancel
      </button>
    </div>
  )
}
