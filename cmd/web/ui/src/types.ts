// Docker resource types mirroring the Go backend responses.

export interface ImageRecord {
  DockerID: string
  ShortID: string
  RepoTags: string[] | null
  RepoDigests: string[] | null
  OS: string
  Architecture: string
  Variant: string
  Size: number
  ImageCreatedAt: string
  RegisteredAt: string
  UpdatedAt: string
  Origin: 'in_band' | 'out_of_band'
  Status: 'present' | 'deleted' | 'deleted_out_of_band' | 'errored'
  ErrMsg: string
}

export interface Container {
  Id: string
  Names: string[]
  Image: string
  ImageID: string
  Command: string
  Created: number
  Status: string
  State: string
  Ports: Port[]
  SizeRw?: number
  SizeRootFs?: number
}

export interface Port {
  IP: string
  PrivatePort: number
  PublicPort: number
  Type: string
}

export interface ContainerDetail {
  Id: string
  Name: string
  Image: string
  Platform: string
  Config?: { Image: string; Cmd: string[] | null }
  State?: { Status: string; Running: boolean; StartedAt: string }
  NetworkSettings?: { Networks: Record<string, unknown> }
  Mounts?: Mount[]
}

export interface Mount {
  Type: string
  Source: string
  Destination: string
  Mode: string
}

export interface VolumeRecord {
  Name: string
  Driver: string
  Mountpoint: string
  Scope: string
  Status: string
  CreatedAt?: string
  RegisteredAt?: string
  UpdatedAt?: string
}

export interface NetworkRecord {
  ID?: string
  Name?: string
  Driver?: string
  Scope?: string
  Status?: string
  RegisteredAt?: string
  UpdatedAt?: string
}

export interface StateSummary {
  requested: number
  active: number
  failed: number
  abandoned: number
}

export interface AllState {
  images: StateSummary
  containers: StateSummary
  volumes: StateSummary
  networks: StateSummary
  builds: StateSummary
  execs: StateSummary
}

export interface HealthStatus {
  ok: boolean
  docker: boolean
  error?: string
}

export type Tab = 'images' | 'containers' | 'volumes' | 'networks' | 'execs' | 'terminal'

// ── Web Containers ─────────────────────────────────────────────────────────

export interface CatalogEntry {
  id: string
  image: string
  display_name: string
  description: string
}

export interface WebSession {
  user_id: string
  catalog_id: string
  container_name: string
  container_id: string
  status: 'provisioning' | 'ready' | 'terminating' | 'stopped' | 'error'
  error?: string
  terminal_path?: string
}

// ── ProxyPortMap ───────────────────────────────────────────────────────────

export interface MappedPort {
  container_port: number
  host_port: number
  url: string
}

export interface PortScanResult {
  user_id: string
  container_name: string
  ports: MappedPort[]
  scanned_at: string
}
