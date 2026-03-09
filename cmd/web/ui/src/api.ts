// API client — thin wrappers around fetch() for all dokoko endpoints.

const BASE = '/api'

async function request<T>(method: string, path: string, body?: unknown): Promise<T> {
  const opts: RequestInit = {
    method,
    headers: body ? { 'Content-Type': 'application/json' } : {},
    body: body ? JSON.stringify(body) : undefined,
  }
  const res = await fetch(`${BASE}${path}`, opts)
  const json = await res.json()
  if (!res.ok) throw new Error(json.error ?? `HTTP ${res.status}`)
  return (json.data ?? json.message ?? json) as T
}

const get  = <T>(path: string) => request<T>('GET', path)
const post = <T>(path: string, body?: unknown) => request<T>('POST', path, body)
const del  = <T>(path: string) => request<T>('DELETE', path)

// ── Auth ─────────────────────────────────────────────────────────────────────
export const loginApi    = (username: string, password: string) =>
  post<{ username: string; role: string }>('/auth/login', { username, password })
export const logoutApi   = () => post('/auth/logout')
export const meApi       = () => get<{ username: string; role: string }>('/auth/me')
export const registerApi = (username: string, password: string, confirm_password: string) =>
  post<{ username: string; role: string }>('/auth/register', { username, password, confirm_password })

// ── Health ───────────────────────────────────────────────────────────────────
export const health = () => get<{ ok: boolean; docker: boolean; error?: string }>('/health')

// ── Images ───────────────────────────────────────────────────────────────────
export const listImages  = () => get('/images')
export const pullImage   = (ref: string, platform = '') => post('/images/pull', { ref, platform })
export const removeImage = (id: string, force = true) => post('/images/remove', { id, force })
export const tagImage    = (source: string, target: string) => post('/images/tag', { source, target })
export const refreshImages = () => post('/images/refresh')
export const inspectImage  = (id: string) => get(`/images/${encodeURIComponent(id)}/inspect`)

// ── Containers ───────────────────────────────────────────────────────────────
export const listContainers   = () => get('/containers')
export const createContainer  = (image: string, name: string, run = true) => post('/containers', { image, name, run })
export const startContainer   = (id: string) => post(`/containers/${id}/start`)
export const stopContainer    = (id: string) => post(`/containers/${id}/stop`)
export const removeContainer  = (id: string) => del(`/containers/${id}`)
export const inspectContainer = (id: string) => get(`/containers/${id}/inspect`)

// ── Volumes ───────────────────────────────────────────────────────────────────
export const listVolumes   = () => get('/volumes')
export const createVolume  = (name: string, driver = 'local') => post('/volumes', { name, driver })
export const removeVolume  = (name: string) => del(`/volumes/${encodeURIComponent(name)}`)
export const pruneVolumes  = () => post('/volumes/prune')
export const refreshVolumes = () => post('/volumes/refresh')
export const inspectVolume = (name: string) => get(`/volumes/${encodeURIComponent(name)}/inspect`)

// ── Networks ──────────────────────────────────────────────────────────────────
export const listNetworks   = () => get('/networks')
export const createNetwork  = (name: string, driver = 'bridge') => post('/networks', { name, driver })
export const removeNetwork  = (id: string) => del(`/networks/${encodeURIComponent(id)}`)
export const pruneNetworks  = () => post('/networks/prune')
export const refreshNetworks = () => post('/networks/refresh')
export const inspectNetwork = (id: string) => get(`/networks/${encodeURIComponent(id)}/inspect`)

// ── Execs ─────────────────────────────────────────────────────────────────────
export const createExec  = (container: string, cmd: string) => post<{ exec_id: string; container: string }>('/execs', { container, cmd })
export const startExec   = (id: string, detach = true) => post(`/execs/${id}/start`, { detach })
export const inspectExec = (id: string) => get(`/execs/${id}/inspect`)

// ── State ─────────────────────────────────────────────────────────────────────
export const getState = () => get('/state')

// ── Web Containers ────────────────────────────────────────────────────────────
export const listWebCatalog   = () => get('/webcontainers/catalog')
export const getImageVars     = (catalog_id: string) =>
  get<{ vars: import('./types.ts').VarDef[] }>(`/webcontainers/imagevars/${encodeURIComponent(catalog_id)}`)
export const provisionWeb     = (user_id: string, catalog_id: string) =>
  post('/webcontainers/provision', { user_id, catalog_id })
export const getWebSession    = (user_id: string) =>
  get(`/webcontainers/session/${encodeURIComponent(user_id)}`)
export const terminateWeb     = (user_id: string) =>
  del(`/webcontainers/session/${encodeURIComponent(user_id)}`)

// ── Web Container Env Vars ────────────────────────────────────────────────────
export const getContainerEnv = (user_id: string) =>
  get<Record<string, string>>(`/webcontainers/env/${encodeURIComponent(user_id)}`)
export const setContainerEnv = (user_id: string, vars: Record<string, string>) =>
  post<Record<string, string>>(`/webcontainers/env/${encodeURIComponent(user_id)}`, vars)

// ── ProxyPortMap ──────────────────────────────────────────────────────────────
export const scanPorts        = (user_id: string) =>
  post('/proxyportmap/scan', { user_id })
export const getPortMappings  = (user_id: string) =>
  get(`/proxyportmap/mappings/${encodeURIComponent(user_id)}`)
export const removePortMappings = (user_id: string) =>
  del(`/proxyportmap/mappings/${encodeURIComponent(user_id)}`)

// ── User management (admin only) ──────────────────────────────────────────────
import type { UserRecord } from './types.ts'
export const listUsers          = () => get<UserRecord[]>('/users')
export const createUserApi      = (username: string, password: string, role: string) =>
  post<UserRecord>('/users', { username, password, role })
export const deleteUserApi      = (username: string) =>
  del(`/users/${encodeURIComponent(username)}`)
export const updatePasswordApi  = (username: string, password: string) =>
  request<unknown>('PUT', `/users/${encodeURIComponent(username)}/password`, { password })
