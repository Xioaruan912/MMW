// 与主控 HTTP API 的薄封装。所有请求都带 cookie（同源）。
export interface User {
  id: string
  username: string
  nickname: string
  role: string
}

export interface Metrics {
  ts: number
  cpu_percent: number
  mem_used: number
  mem_total: number
  disk_used: number
  disk_total: number
  load1: number
  net_up: number
  net_down: number
  tcp_conns: number
  uptime_sec: number
  xray_running: boolean
}

export interface Server {
  id: string
  name: string
  address: string
  remark: string
  xray_mode: string
  status: string
  agent_version: string
  last_seen_human: string
  online: boolean
  metrics?: Metrics
}

export interface Inbound {
  id: string
  server_id: string
  tag: string
  protocol: string
  transport: string
  security: string
  port: number
  listen: string
  settings: string
  stream: string
  enabled: boolean
  remark: string
}

export interface InboundClient {
  id: string
  inbound_id: string
  user_id: string
  uuid: string
  email: string
  enabled: boolean
}

export interface NodeItem {
  id: string
  inbound_id: string
  name: string
  region: string
  tags: string
  sort: number
  enabled: boolean
  inbound: Inbound
  server_name: string
  server_address: string
}

export interface UserItem {
  id: string
  username: string
  nickname: string
  email: string
  role: string
  status: string
  sub_token: string
  traffic_limit: number
  traffic_used: number
  expire_at: number
  created_at: number
}

export interface XrayPreview {
  rev: string
  tags: string[]
  config: unknown
}

export interface ApplyResult {
  server_id: string
  rev: string
  inbounds: number
  status: string
  applied: boolean
  output?: string
  error?: string
}

export interface AuditLog {
  id: string
  ts: number
  actor: string
  action: string
  target: string
  detail: string
}

export class ApiError extends Error {
  constructor(
    public status: number,
    public code: string,
    message: string,
  ) {
    super(message)
  }
}

function csrfToken(): string {
  const m = document.cookie.match(/(?:^|;\s*)mmx_csrf=([^;]*)/)
  return m ? decodeURIComponent(m[1]) : ''
}

async function request<T>(path: string, init?: RequestInit): Promise<T> {
  const method = (init?.method ?? 'GET').toUpperCase()
  const headers: Record<string, string> = {
    'Content-Type': 'application/json',
    ...((init?.headers as Record<string, string>) ?? {}),
  }
  // 状态变更请求必须带 CSRF 令牌（double-submit cookie）
  if (!['GET', 'HEAD', 'OPTIONS'].includes(method)) {
    const t = csrfToken()
    if (t) headers['X-CSRF-Token'] = t
  }
  const res = await fetch(path, {
    credentials: 'same-origin',
    headers,
    ...init,
  })
  const text = await res.text()
  const body = text ? JSON.parse(text) : {}
  if (!res.ok) {
    throw new ApiError(res.status, body.code ?? 'error', body.message ?? `HTTP ${res.status}`)
  }
  return body as T
}

export const api = {
  setupStatus: () => request<{ needs_init: boolean; version: string }>('/api/setup/status'),
  setupInit: (username: string, password: string) =>
    request<{ user: User }>('/api/setup/init', {
      method: 'POST',
      body: JSON.stringify({ username, password }),
    }),
  login: (username: string, password: string) =>
    request<{ user: User }>('/api/login', {
      method: 'POST',
      body: JSON.stringify({ username, password }),
    }),
  logout: () => request<{ ok: boolean }>('/api/logout', { method: 'POST' }),
  me: () => request<{ user: User }>('/api/me'),

  listServers: () => request<{ servers: Server[]; total: number }>('/api/admin/servers'),
  createServer: (name: string, address: string, xray_mode: string) =>
    request<{ server: Server; enroll_token: string; install_cmd: string }>('/api/admin/servers', {
      method: 'POST',
      body: JSON.stringify({ name, address, xray_mode }),
    }),
  deleteServer: (id: string) => request<{ ok: boolean }>(`/api/admin/servers/${id}`, { method: 'DELETE' }),
  installCommand: (id: string) =>
    request<{ install_cmd: string; enroll_token: string }>(`/api/admin/servers/${id}/install-command`, {
      method: 'POST',
    }),
  dispatchTask: (id: string, kind: string, args: Record<string, unknown> = {}) =>
    request<{ result: { status: string; output?: string; error?: string } }>(
      `/api/admin/servers/${id}/tasks`,
      { method: 'POST', body: JSON.stringify({ kind, args }) },
    ),
  taskKinds: () => request<{ kinds: string[] }>('/api/admin/tasks/kinds'),
  audit: () => request<{ logs: AuditLog[] }>('/api/admin/audit'),

  // ---- 入站 ----
  listInbounds: (serverId?: string) =>
    request<{
      inbounds: Inbound[]
      protocols: string[]
      transports: string[]
      securities: string[]
    }>(`/api/admin/inbounds${serverId ? `?server_id=${serverId}` : ''}`),
  createInbound: (body: Record<string, unknown>) =>
    request<{ inbound: Inbound; node: unknown }>('/api/admin/inbounds', {
      method: 'POST',
      body: JSON.stringify(body),
    }),
  deleteInbound: (id: string) =>
    request<{ ok: boolean }>(`/api/admin/inbounds/${id}`, { method: 'DELETE' }),
  listClients: (inboundId: string) =>
    request<{ clients: InboundClient[] }>(`/api/admin/inbounds/${inboundId}/clients`),
  addClient: (inboundId: string, userId: string, email = '') =>
    request<{ client: InboundClient }>(`/api/admin/inbounds/${inboundId}/clients`, {
      method: 'POST',
      body: JSON.stringify({ user_id: userId, email }),
    }),
  deleteClient: (id: string) =>
    request<{ ok: boolean }>(`/api/admin/clients/${id}`, { method: 'DELETE' }),

  // ---- 节点 ----
  listNodes: () => request<{ nodes: NodeItem[]; total: number }>('/api/admin/nodes'),
  updateNode: (id: string, body: Record<string, unknown>) =>
    request<{ node: unknown }>(`/api/admin/nodes/${id}`, {
      method: 'PATCH',
      body: JSON.stringify(body),
    }),

  // ---- Xray ----
  xrayPreview: (serverId: string) =>
    request<XrayPreview>(`/api/admin/servers/${serverId}/xray-preview`),
  applyXray: (serverId: string) =>
    request<{ result: ApplyResult }>(`/api/admin/servers/${serverId}/apply`, { method: 'POST' }),

  // ---- 用户 ----
  listUsers: () => request<{ users: UserItem[]; total: number }>('/api/admin/users'),
  createUser: (username: string, password: string, nickname = '') =>
    request<{ user: UserItem }>('/api/admin/users', {
      method: 'POST',
      body: JSON.stringify({ username, password, nickname }),
    }),
  deleteUser: (id: string) => request<{ ok: boolean }>(`/api/admin/users/${id}`, { method: 'DELETE' }),
  rotateSubToken: (id: string) =>
    request<{ sub_token: string; sub_url: string }>(`/api/admin/users/${id}/sub-token`, {
      method: 'POST',
    }),
  subURL: (token: string) => `${location.origin}/sub/${token}`,
  overview: () =>
    request<{
      servers_total: number
      servers_online: number
      agents_connected: number
      users_total: number
      version: string
      uptime_sec: number
    }>('/api/admin/overview'),
}

export function fmtBytes(n: number | undefined): string {
  if (!n || n <= 0) return '0 B'
  const units = ['B', 'KB', 'MB', 'GB', 'TB']
  const i = Math.min(Math.floor(Math.log(n) / Math.log(1024)), units.length - 1)
  return `${(n / Math.pow(1024, i)).toFixed(i === 0 ? 0 : 1)} ${units[i]}`
}

export function fmtPct(n: number | undefined): string {
  return `${(n ?? 0).toFixed(1)}%`
}

export function fmtDate(ms: number | undefined): string {
  if (!ms) return '不限'
  return new Date(ms).toLocaleDateString()
}

export function fmtDuration(sec: number | undefined): string {
  if (!sec) return '-'
  const d = Math.floor(sec / 86400)
  const h = Math.floor((sec % 86400) / 3600)
  const m = Math.floor((sec % 3600) / 60)
  return d > 0 ? `${d}天${h}小时` : h > 0 ? `${h}小时${m}分` : `${m}分`
}
