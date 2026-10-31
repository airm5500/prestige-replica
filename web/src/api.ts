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
  backupEnabled: boolean; backupNodeId: string | null; restoreNodeId: string | null; backupHour: number; backupKeep: number
  restoreTestWeekday: number; backupMaxAgeHours: number; drillMaxAgeDays: number
  binlogArchiveEnabled: boolean; binlogArchiveMinutes: number; binlogArchiveNodeId: string | null; binlogGapAt: string | null; binlogGapDetail: string | null
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
  openIncidents: number; worstSeverity: Severity | null; capabilities: string[]
  replicationPausedAt: string | null; replicationPausedBy: string | null; replicationPauseReason: string | null
  fenceRequired: boolean; fenceReason: string | null; router: RouterStatus | null
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

export type OperationType = 'PREFLIGHT' | 'BACKUP_FULL' | 'RESTORE_TEST' | 'BACKUP_PRUNE' | 'PROVISION_REPLICA'
  | 'REPLICATION_PAUSE' | 'REPLICATION_RESUME' | 'SWITCHOVER' | 'SWITCHOVER_DEMOTE' | 'SWITCHOVER_PROMOTE' | 'SWITCHOVER_UNDO' | 'REPLICA_ATTACH' | 'FAILOVER' | 'FENCE' | 'DRILL'
  | 'BINLOG_ARCHIVE' | 'PITR_RESTORE'
export type OperationStatus = 'AWAITING_APPROVAL' | 'QUEUED' | 'RUNNING' | 'SUCCEEDED' | 'FAILED' | 'INTERVENTION_REQUIRED' | 'CANCELLED'
export interface Operation {
  id: string; tenantId: string; siteId: string; topologyId: string | null; nodeId: string; type: OperationType; status: OperationStatus
  params: unknown; summary: string; requiresApproval: boolean; requestedBy: string; requestedAt: string; approvedBy: string | null
  approvedAt: string | null; startedAt: string | null; finishedAt: string | null; lastEventAt: string | null; error: string | null; result: unknown
  parentId: string | null
}
export interface OperationEvent { id: number; operationId: string; at: string; level: 'INFO' | 'WARN' | 'ERROR'; message: string }
export interface OperationView {
  operation: Operation; events: OperationEvent[]; nodeName: string; siteCode: string; siteName: string | null
  topologyName: string | null; steps: OperationView[]
}
export interface Check { id: string; status: 'OK' | 'WARN' | 'FAIL'; message: string }
export interface RouterTarget { address: string; reachable: boolean; readOnly: boolean; replicating: boolean; serverId: number; error?: string }
export interface RouterStatus {
  listen: string; current: string; reason: string; targets: RouterTarget[]; activeConnections: number
  totalConnections: number; refusedConnections: number; lastChangeAt: string; splitBrainEvents: number
}
export interface FailoverPlan {
  ready: boolean; checks: Check[]; oldPrimary: string | null; targetId: string | null; target: string | null
  oldAgentAlive: boolean; manualFencingRequired: boolean; lossEstimate: string | null; repointed: string[]
}
export interface SwitchoverPlan { ready: boolean; checks: Check[]; oldPrimary: string | null; target: string | null; newSource: string | null; repointed: string[] }
export interface RepairPlan {
  diagnosis: string; detail: string; eligibleBackups: string[]
  actions: { id: 'RESUME' | 'REBUILD' | 'WAIT'; label: string; detail: string; recommended: boolean }[]
}
export interface Backup {
  id: string; siteId: string; topologyId: string | null; nodeId: string; operationId: string | null; kind: string; relativeDir: string
  status: 'RUNNING' | 'COMPLETED' | 'FAILED' | 'DELETED'; startedAt: string; finishedAt: string | null; sizeBytes: number | null
  sha256: string | null; keyId: string | null; gtid: string | null; serverVersion: string | null
  verifyStatus: 'NOT_VERIFIED' | 'VERIFIED' | 'FAILED'; verifiedAt: string | null; restoreSeconds: number | null
}
export interface RestoreTest { id: string; backupId: string; operationId: string; nodeId: string; status: string; at: string; totalSeconds: number | null; gtid: string | null; details: unknown }
export interface BackupPolicy {
  backupEnabled: boolean; backupNodeId: string | null; restoreNodeId: string | null; backupHour: number; backupKeep: number
  restoreTestWeekday: number; backupMaxAgeHours: number
}

export interface BinlogFile {
  id: string; serverId: number; fileName: string; relativePath: string; sizeBytes: number | null; firstGtid: string | null; lastGtid: string | null
  firstEventAt: string | null; lastEventAt: string | null; complete: boolean; archivedAt: string
}
export interface BinlogCatalog {
  files: BinlogFile[]; coveredFrom: string | null; coveredUntil: string | null; totalBytes: number
  gapAt: string | null; gapDetail: string | null; lastRun: Operation | null
}
export interface PitrPlan {
  ready: boolean; checks: Check[]; backupId: string | null; backupAt: string | null; backupGtid: string | null
  executorId: string | null; executor: string | null; coveredUntil: string | null
  binlogs: { relativePath: string; fileName: string; serverId: number; firstEventAt: string | null; lastEventAt: string | null }[]
}

/** Les colonnes JSON peuvent arriver sérialisées en texte. */
export function json<T = any>(v: unknown): T | null {
  if (v === null || v === undefined || v === '') return null
  if (typeof v === 'string') { try { return JSON.parse(v) as T } catch { return null } }
  return v as T
}

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

  // Lot 2 : opérations, sauvegardes, réplique
  preflight: (nodeId: string) => request<Operation>('POST', `api/v1/nodes/${nodeId}/preflight`),
  backupNow: (nodeId: string) => request<Operation>('POST', `api/v1/nodes/${nodeId}/backups`),
  verifyBackup: (backupId: string, nodeId: string | null) => request<Operation>('POST', `api/v1/backups/${backupId}/verify`, { nodeId }),
  backups: (topologyId?: string) => request<Backup[]>('GET', `api/v1/backups${topologyId ? `?topologyId=${topologyId}` : ''}`),
  restoreTests: (backupId: string) => request<RestoreTest[]>('GET', `api/v1/backups/${backupId}/restore-tests`),
  setBackupPolicy: (topologyId: string, b: BackupPolicy) => request<Topology>('PUT', `api/v1/topologies/${topologyId}/backup-policy`, b),
  provision: (topologyId: string, b: { targetNodeId: string; backupId: string; confirmDataDir: string; delaySeconds: number; topologyConfirmation: string }) =>
    request<Operation>('POST', `api/v1/topologies/${topologyId}/replicas/provision`, b),
  operations: (q: { status?: string; nodeId?: string; topologyId?: string; type?: OperationType; limit?: number } = {}) => {
    const p = new URLSearchParams()
    Object.entries(q).forEach(([k, v]) => { if (v !== undefined && v !== '') p.set(k, String(v)) })
    return request<Operation[]>('GET', `api/v1/operations?${p}`)
  },
  operation: (id: string) => request<OperationView>('GET', `api/v1/operations/${id}`),
  approve: (id: string, confirmation: string) => request<Operation>('POST', `api/v1/operations/${id}/approve`, { confirmation }),
  cancelOperation: (id: string) => request<Operation>('POST', `api/v1/operations/${id}/cancel`),

  // Lot 3 : opérations contrôlées
  pause: (nodeId: string, mode: 'ALL' | 'SQL', reason: string) => request<Operation>('POST', `api/v1/nodes/${nodeId}/replication/pause`, { mode, reason }),
  resume: (nodeId: string) => request<Operation>('POST', `api/v1/nodes/${nodeId}/replication/resume`),
  attach: (nodeId: string) => request<Operation>('POST', `api/v1/nodes/${nodeId}/replication/attach`),
  repairPlan: (nodeId: string) => request<RepairPlan>('GET', `api/v1/nodes/${nodeId}/repair-plan`),
  failoverPlan: (topologyId: string, targetNodeId: string | null) =>
    request<FailoverPlan>('POST', `api/v1/topologies/${topologyId}/failover/plan`, { targetNodeId }),
  failover: (topologyId: string, b: { targetNodeId: string | null; fencingConfirmed: boolean; topologyConfirmation: string }) =>
    request<Operation>('POST', `api/v1/topologies/${topologyId}/failover`, b),
  drill: (topologyId: string, b: { targetNodeId: string; notBefore: string | null; topologyConfirmation: string }) =>
    request<Operation>('POST', `api/v1/topologies/${topologyId}/drill`, b),
  drillPolicy: (topologyId: string, drillMaxAgeDays: number) =>
    request<Topology>('PUT', `api/v1/topologies/${topologyId}/drill-policy`, { drillMaxAgeDays }),
  declareManualFailover: (topologyId: string, b: { newPrimaryNodeId: string; topologyConfirmation: string }) =>
    request<void>('POST', `api/v1/topologies/${topologyId}/failover/declare-manual`, b),
  binlogPolicy: (topologyId: string, b: { binlogArchiveEnabled: boolean; binlogArchiveNodeId: string | null; binlogArchiveMinutes: number }) =>
    request<Topology>('PUT', `api/v1/topologies/${topologyId}/binlog-policy`, b),
  binlogs: (topologyId: string) => request<BinlogCatalog>('GET', `api/v1/topologies/${topologyId}/binlogs`),
  pitrPlan: (topologyId: string, stopAt: string) => request<PitrPlan>('POST', `api/v1/topologies/${topologyId}/pitr/plan`, { stopAt }),
  pitr: (topologyId: string, b: { stopAt: string; extract: string[]; reason: string }) =>
    request<Operation>('POST', `api/v1/topologies/${topologyId}/pitr`, b),
  switchoverPlan: (topologyId: string, targetNodeId: string) => request<SwitchoverPlan>('POST', `api/v1/topologies/${topologyId}/switchover/plan`, { targetNodeId }),
  switchover: (topologyId: string, b: { targetNodeId: string; reattachOldPrimary: boolean; topologyConfirmation: string }) =>
    request<Operation>('POST', `api/v1/topologies/${topologyId}/switchover`, b),
}
