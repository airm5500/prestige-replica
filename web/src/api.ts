// Client de l'API REST v1. Les cookies de session sont httpOnly : le code
// JavaScript ne manipule jamais d'identifiant ni de mot de passe stocké.

export type Severity = 'INFO' | 'WARNING' | 'CRITICAL'
export type Health = 'HEALTHY' | 'DEGRADED' | 'CRITICAL' | 'MAINTENANCE' | 'UNKNOWN'
export type NodeRole = 'PRIMARY' | 'REPLICA' | 'DELAYED_REPLICA' | 'STANDALONE'

export interface Me { username: string; displayName: string | null; role: string; tenantId: string | null; siteId: string | null; global: boolean }
export interface Tenant { id: string; code: string; name: string }
export interface Site { id: string; tenantId: string; code: string; name: string; environment: string; color: string; timezone: string }
export interface Topology {
  id: string; tenantId: string; siteId: string; name: string; mode: string; maintenance: boolean
  lagWarnSeconds: number; lagCritSeconds: number; diskWarnPct: number; diskCritPct: number; agentSilenceSeconds: number
}
export interface ChannelView {
  name: string; source: string; ioRunning: string; sqlRunning: string; lagSeconds: number | null; sqlDelay: number
  usingGtid: string; gtidIoPos: string; lastIoErrno: number; lastIoError: string; lastSqlErrno: number; lastSqlError: string
  relayLogBytes: number; sqlState: string
}
export interface NodeSummary {
  id: string; name: string; expectedRole: NodeRole; topologyId: string | null; siteId: string; addressLabel: string | null
  status: Health; lastSeenAt: string | null; lastReportAt: string | null; reachable: boolean; version: string | null
  serverId: number | null; readOnly: boolean | null; gtidCurrentPos: string | null; gtidSlavePos: string | null
  channels: ChannelView[]; minDiskFreePct: number | null; agentVersion: string | null; agentTokenPrefix: string
  openIncidents: number; worstSeverity: Severity | null
}
export interface TopologySummary { id: string; name: string; mode: string; maintenance: boolean; status: Health; primary: string | null; rpoSecondsEstimate: number | null; nodes: NodeSummary[] }
export interface SiteSummary {
  id: string; tenantId: string; code: string; name: string; environment: string; color: string; status: Health
  openIncidents: number; criticalIncidents: number; topologies: TopologySummary[]; unassignedNodes: NodeSummary[]
}
export interface Incident {
  id: string; siteId: string; topologyId: string | null; nodeId: string | null; type: string; severity: Severity
  status: 'OPEN' | 'ACKNOWLEDGED' | 'RESOLVED'; message: string; details: string | null; openedAt: string; lastSeenAt: string
  acknowledgedAt: string | null; acknowledgedBy: string | null; assignee: string | null; resolvedAt: string | null
}
export interface AuditEvent { id: number; at: string; tenantId: string | null; siteId: string | null; actor: string; action: string; resource: string | null; result: string; details: string | null; hash: string }
export interface NodeMetric { collectedAt: string; reachable: boolean; lagSeconds: number | null; ioRunning: boolean | null; sqlRunning: boolean | null; binlogBytes: number | null; relayLogBytes: number | null; minDiskFreePct: number | null; threadsConnected: number | null }
export interface NotificationChannel { id: string; tenantId: string; siteId: string | null; type: 'EMAIL' | 'WEBHOOK'; target: string; minSeverity: Severity; enabled: boolean }
export interface UserOut { id: string; username: string; role: string; tenantId: string | null; siteId: string | null; displayName: string | null; enabled: boolean }

export class ApiError extends Error {
  constructor(public status: number, message: string) { super(message) }
}

async function request<T>(method: string, path: string, body?: unknown): Promise<T> {
  const res = await fetch(path, {
    method,
    credentials: 'same-origin',
    headers: body === undefined ? { Accept: 'application/json' } : { 'Content-Type': 'application/json', Accept: 'application/json' },
    body: body === undefined ? undefined : JSON.stringify(body),
  })
  if (res.status === 401) {
    window.dispatchEvent(new CustomEvent('prm:unauthorized'))
    throw new ApiError(401, 'Session expirée, reconnectez-vous')
  }
  if (!res.ok) {
    let msg = `Erreur ${res.status}`
    try {
      const j = await res.json()
      msg = j.title ?? j.message ?? (Array.isArray(j.violations) ? j.violations.map((v: { message: string }) => v.message).join(', ') : msg)
    } catch { /* corps non JSON */ }
    throw new ApiError(res.status, msg)
  }
  if (res.status === 204) return undefined as T
  const ct = res.headers.get('content-type') ?? ''
  return (ct.includes('json') ? res.json() : res.text()) as Promise<T>
}

export const api = {
  async login(username: string, password: string): Promise<void> {
    const form = new URLSearchParams({ j_username: username, j_password: password })
    const res = await fetch('j_security_check', { method: 'POST', body: form, credentials: 'same-origin', redirect: 'manual' })
    if (res.status !== 200 && res.status !== 302 && res.type !== 'opaqueredirect') throw new ApiError(res.status, 'Identifiant ou mot de passe incorrect')
  },
  logout: () => request<void>('POST', 'api/v1/logout'),
  me: () => request<Me>('GET', 'api/v1/me'),
  fleet: () => request<SiteSummary[]>('GET', 'api/v1/fleet'),
  tenants: () => request<Tenant[]>('GET', 'api/v1/tenants'),
  createTenant: (b: { code: string; name: string }) => request<Tenant>('POST', 'api/v1/tenants', b),
  sites: () => request<Site[]>('GET', 'api/v1/sites'),
  createSite: (b: Partial<Site>) => request<Site>('POST', 'api/v1/sites', b),
  topologies: () => request<Topology[]>('GET', 'api/v1/topologies'),
  createTopology: (b: Partial<Topology>) => request<Topology>('POST', 'api/v1/topologies', b),
  updateTopology: (id: string, b: Partial<Topology>) => request<Topology>('PUT', `api/v1/topologies/${id}`, b),
  registerNode: (b: { siteId: string; topologyId: string | null; name: string; expectedRole: NodeRole; addressLabel: string }) =>
    request<{ nodeId: string; agentToken: string; warning: string }>('POST', 'api/v1/nodes/register', b),
  rotateToken: (id: string) => request<{ nodeId: string; agentToken: string; warning: string }>('POST', `api/v1/nodes/${id}/rotate-token`),
  node: (id: string) => request<{ summary: NodeSummary; report: Record<string, any> | null; incidents: Incident[]; site: Site; topology: Topology | null }>('GET', `api/v1/nodes/${id}`),
  metrics: (id: string, hours: number) => request<NodeMetric[]>('GET', `api/v1/nodes/${id}/metrics?hours=${hours}`),
  incidents: (status: 'open' | 'all') => request<Incident[]>('GET', `api/v1/incidents?status=${status}`),
  acknowledge: (id: string, assignee: string) => request<Incident>('POST', `api/v1/incidents/${id}/acknowledge`, { assignee }),
  audit: (params: { actor?: string; action?: string; beforeId?: number }) => {
    const q = new URLSearchParams()
    Object.entries(params).forEach(([k, v]) => { if (v !== undefined && v !== '') q.set(k, String(v)) })
    return request<AuditEvent[]>('GET', `api/v1/audit?${q}`)
  },
  verifyAudit: () => request<{ valid: boolean; checked: number; firstBrokenId: number | null }>('GET', 'api/v1/audit/verify'),
  channels: () => request<NotificationChannel[]>('GET', 'api/v1/notification-channels'),
  createChannel: (b: Partial<NotificationChannel>) => request<NotificationChannel>('POST', 'api/v1/notification-channels', b),
  disableChannel: (id: string) => request<void>('DELETE', `api/v1/notification-channels/${id}`),
  users: () => request<UserOut[]>('GET', 'api/v1/users'),
  createUser: (b: Record<string, unknown>) => request<UserOut>('POST', 'api/v1/users', b),
}
