<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref, watch } from 'vue'
import { api, type Incident, type NodeMetric, type NodeSummary, type Operation, type RepairPlan, type Site, type Topology } from '../api'
import { useRouter } from 'vue-router'
import OperationPanel from '../components/OperationPanel.vue'
import { useSession } from '../stores/session'
import StatusBadge from '../components/StatusBadge.vue'
import Sparkline from '../components/Sparkline.vue'
import { ago, bytes, date, duration, opStatusClass, opStatusLabel, opTypeLabel, pct, roleLabel } from '../format'

const props = defineProps<{ id: string }>()
const summary = ref<NodeSummary | null>(null)
const report = ref<Record<string, any> | null>(null)
const incidents = ref<Incident[]>([])
const site = ref<Site | null>(null)
const topology = ref<Topology | null>(null)
const metrics = ref<NodeMetric[]>([])
const hours = ref(24)
const error = ref('')
const ops = ref<Operation[]>([])
const shownOp = ref('')
const session = useSession()
const canOperate = computed(() => session.can('SUPER_ADMIN', 'SITE_ADMIN', 'OPERATOR'))
const router = useRouter()
const repair = ref<RepairPlan | null>(null)
const pauseMode = ref<'ALL' | 'SQL'>('ALL')
const pauseReason = ref('')
const isReplica = computed(() => summary.value?.expectedRole === 'REPLICA' || summary.value?.expectedRole === 'DELAYED_REPLICA')
const canControl = computed(() => !!summary.value?.capabilities?.includes('replicationControl'))
const unhealthy = computed(() => {
  const s = summary.value
  if (!s || !isReplica.value) return false
  return !!s.replicationPausedAt || !s.channels.length || s.channels.some((c) => c.ioRunning !== 'Yes' || c.sqlRunning !== 'Yes' || c.lastIoErrno || c.lastSqlErrno)
})
async function loadRepair() {
  repair.value = unhealthy.value ? await api.repairPlan(props.id).catch(() => null) : null
}
function doRepair(action: string) {
  if (action === 'RESUME') start(api.resume)
  else if (action === 'REBUILD') router.push({ path: '/backups', query: { rebuild: props.id } })
}
const pause = () => start((id) => api.pause(id, pauseMode.value, pauseReason.value)).then(() => { pauseReason.value = '' })
let timer: number | undefined

async function load() {
  try {
    const d = await api.node(props.id)
    summary.value = d.summary
    report.value = d.report
    incidents.value = d.incidents
    site.value = d.site
    topology.value = d.topology
    metrics.value = await api.metrics(props.id, hours.value)
    ops.value = await api.operations({ nodeId: props.id, limit: 10 })
    await loadRepair()
    if (!shownOp.value) shownOp.value = ops.value.find((o) => o.type === 'PREFLIGHT')?.id ?? ''
    error.value = ''
  } catch (e) { error.value = (e as Error).message }
}
watch(hours, load)
onMounted(() => { load(); timer = window.setInterval(load, 15_000) })
onUnmounted(() => window.clearInterval(timer))

async function start(fn: (id: string) => Promise<Operation>) {
  error.value = ''
  try { const o = await fn(props.id); shownOp.value = o.id; await load() } catch (e) { error.value = (e as Error).message }
}

const server = computed(() => report.value?.server ?? null)
const config = computed(() => {
  const s = server.value
  if (!s) return []
  return [
    ['server_id', s.serverId], ['read_only', s.readOnly ? 'ON' : 'OFF'], ['log_bin', s.logBin ? 'ON' : 'OFF'],
    ['log_slave_updates', s.logSlaveUpdates ? 'ON' : 'OFF'], ['binlog_format', s.binlogFormat], ['binlog_row_image', s.binlogRowImage],
    ['gtid_domain_id', s.gtidDomainId], ['gtid_strict_mode', s.gtidStrictMode ? 'ON' : 'OFF'], ['gtid_current_pos', s.gtidCurrentPos],
    ['gtid_binlog_pos', s.gtidBinlogPos], ['gtid_slave_pos', s.gtidSlavePos], ['rétention binlogs', duration(s.binlogExpireSeconds)],
    ['sync_binlog', s.syncBinlog], ['innodb_flush_log_at_trx_commit', s.innodbFlushLogAtTrxCommit],
    ['semi-sync primaire / réplique', `${s.semiSyncMasterEnabled ? 'ON' : 'OFF'} / ${s.semiSyncSlaveEnabled ? 'ON' : 'OFF'}`],
    ['time_zone', `${s.timeZone} (système ${s.systemTimeZone})`], ['have_ssl', s.haveSsl], ['datadir', s.dataDir],
    ['uptime', duration(s.uptimeSeconds)], ['connexions', `${s.threadsConnected} / ${s.maxConnections}`],
  ] as [string, unknown][]
})
</script>

<template>
  <section v-if="summary">
    <p><RouterLink to="/">← Parc</RouterLink></p>
    <h1>{{ summary.name }} <StatusBadge :health="summary.status" /></h1>
    <p class="muted">
      {{ roleLabel[summary.expectedRole] }} · {{ site?.name }} [{{ site?.code }}] · topologie {{ topology?.name ?? 'aucune' }}
      · {{ summary.addressLabel ?? '' }} · agent {{ summary.agentVersion ?? '—' }} ({{ report?.hostname ?? '?' }}, {{ report?.os }}/{{ report?.arch }})
      · jeton {{ summary.agentTokenPrefix }}… · dernier contact {{ ago(summary.lastSeenAt) }}
    </p>
    <p v-if="error" class="error" role="alert">{{ error }}</p>
    <p v-if="!report" class="muted">L'agent n'a encore envoyé aucun rapport.</p>
    <div v-if="canOperate" class="toolbar">
      <button @click="start(api.preflight)">Vérifier les prérequis</button>
      <button @click="start(api.backupNow)">Sauvegarder maintenant</button>
      <RouterLink to="/backups">Sauvegardes et répliques →</RouterLink>
    </div>

    <div v-if="isReplica" class="card">
      <h2>Réplication</h2>
      <div v-if="summary.replicationPausedAt" class="pause-box" role="status">
        Suspendue par <strong>{{ summary.replicationPausedBy }}</strong> le {{ date(summary.replicationPausedAt) }}
        <span v-if="summary.replicationPauseReason"> — {{ summary.replicationPauseReason }}</span>.
        Les alertes « thread arrêté » sont remplacées par ce rappel jusqu'à la reprise.
      </div>
      <div v-if="repair" class="warn-box">
        <strong>{{ repair.diagnosis }}</strong>
        <p class="small">{{ repair.detail }}</p>
        <ul class="plain">
          <li v-for="a in repair.actions" :key="a.id">
            <button v-if="canOperate && a.id !== 'WAIT'" :class="a.recommended ? 'primary' : ''" :disabled="a.id === 'REBUILD' && !repair.eligibleBackups.length"
                    @click="doRepair(a.id)">{{ a.label }}</button>
            <strong v-else>{{ a.label }}</strong>
            <span v-if="a.recommended" class="badge healthy">recommandé</span>
            <div class="muted small">{{ a.detail }}</div>
          </li>
        </ul>
      </div>
      <template v-if="canOperate && canControl">
        <div class="toolbar">
          <button v-if="summary.replicationPausedAt" class="primary" @click="start(api.resume)">Reprendre la réplication</button>
          <button v-if="!summary.channels.length" @click="start(api.attach)" title="Brancher ce serveur sur le primaire actuel de la topologie">Rattacher au primaire</button>
        </div>
        <form v-if="!summary.replicationPausedAt && summary.channels.length" class="toolbar" @submit.prevent="pause">
          <label>Suspendre
            <select v-model="pauseMode"><option value="ALL">toute la réplication</option><option value="SQL">l'application seulement (les journaux continuent d'arriver)</option></select>
          </label>
          <label>Motif <input v-model="pauseReason" required maxlength="300" placeholder="ex. sauvegarde à froid, inventaire" /></label>
          <button type="submit">Suspendre</button>
        </form>
      </template>
      <p v-else-if="canOperate" class="muted small">Pilotage non autorisé sur ce serveur : activer <code>replicationControl</code> dans son agent.json.</p>
    </div>

    <div v-if="ops.length" class="grid2">
      <div class="card">
        <h2>Dernières opérations</h2>
        <ul class="plain">
          <li v-for="o in ops" :key="o.id">
            <button class="link" @click="shownOp = o.id">{{ date(o.requestedAt) }} — {{ opTypeLabel[o.type] }}</button>
            <span class="badge" :class="opStatusClass[o.status]">{{ opStatusLabel[o.status] }}</span>
          </li>
        </ul>
      </div>
      <OperationPanel v-if="shownOp" :id="shownOp" @changed="load" />
    </div>

    <div v-if="incidents.length" class="card">
      <h2>Incidents ouverts</h2>
      <ul class="incidents">
        <li v-for="i in incidents" :key="i.id"><StatusBadge :severity="i.severity" /> {{ i.message }} <span class="muted">depuis {{ date(i.openedAt) }}</span>
          <pre v-if="i.details" class="details">{{ i.details }}</pre></li>
      </ul>
    </div>

    <div v-if="summary.channels.length" class="card">
      <h2>Canaux de réplication</h2>
      <div class="table-wrap"><table>
        <thead><tr><th>Canal</th><th>Source</th><th>IO</th><th>SQL</th><th>Retard</th><th>Délai volontaire</th><th>GTID</th><th>Position GTID IO</th><th>Relay log</th><th>État SQL</th></tr></thead>
        <tbody>
          <template v-for="c in summary.channels" :key="c.name">
            <tr>
              <td>{{ c.name }}</td><td>{{ c.source }}</td>
              <td :class="c.ioRunning === 'Yes' ? 'ok' : 'ko'">{{ c.ioRunning }}</td>
              <td :class="c.sqlRunning === 'Yes' ? 'ok' : 'ko'">{{ c.sqlRunning }}</td>
              <td>{{ duration(c.lagSeconds) }}</td><td>{{ c.sqlDelay ? duration(c.sqlDelay) : '—' }}</td>
              <td>{{ c.usingGtid }}</td><td class="mono small">{{ c.gtidIoPos || '—' }}</td>
              <td>{{ bytes(c.relayLogBytes) }}</td><td class="small">{{ c.sqlState }}</td>
            </tr>
            <tr v-if="c.lastIoErrno"><td colspan="10" class="error">Erreur IO {{ c.lastIoErrno }} : {{ c.lastIoError }}</td></tr>
            <tr v-if="c.lastSqlErrno"><td colspan="10" class="error">Erreur SQL {{ c.lastSqlErrno }} : {{ c.lastSqlError }}</td></tr>
          </template>
        </tbody>
      </table></div>
    </div>

    <div class="card">
      <h2>Historique
        <select v-model.number="hours" aria-label="Période">
          <option :value="1">1 h</option><option :value="6">6 h</option><option :value="24">24 h</option><option :value="168">7 j</option>
        </select>
      </h2>
      <div class="grid2">
        <Sparkline :values="metrics.map((m) => m.lagSeconds)" label="Retard de réplication" unit="s" />
        <Sparkline :values="metrics.map((m) => m.minDiskFreePct === null ? null : Math.round(m.minDiskFreePct))" label="Disque libre" unit="%" />
        <Sparkline :values="metrics.map((m) => m.binlogBytes === null ? null : Math.round(m.binlogBytes / 1048576))" label="Binlogs" unit="Mo" />
        <Sparkline :values="metrics.map((m) => m.threadsConnected)" label="Connexions" />
      </div>
    </div>

    <div class="grid2">
      <div v-if="server" class="card">
        <h2>Configuration observée</h2>
        <dl class="kv"><template v-for="[k, v] in config" :key="k"><dt>{{ k }}</dt><dd class="mono">{{ v ?? '—' }}</dd></template></dl>
      </div>
      <div class="card">
        <h2>Stockage et journaux</h2>
        <dl class="kv">
          <template v-if="report?.binlogs">
            <dt>Binlogs</dt><dd>{{ report.binlogs.count }} fichiers, {{ bytes(report.binlogs.totalBytes) }}</dd>
            <dt>Plage</dt><dd class="mono">{{ report.binlogs.first }} → {{ report.binlogs.last }}</dd>
          </template>
          <template v-for="d in report?.disks ?? []" :key="d.path">
            <dt class="mono">{{ d.path }}</dt><dd>{{ pct(100 * d.freeBytes / d.totalBytes) }} libre ({{ bytes(d.freeBytes) }} / {{ bytes(d.totalBytes) }})</dd>
          </template>
          <dt>Bases</dt><dd>{{ server?.databases?.join(', ') || '—' }}</dd>
        </dl>
        <template v-if="report?.errors?.length">
          <h3>Avertissements de collecte</h3>
          <ul><li v-for="e in report.errors" :key="e" class="small">{{ e }}</li></ul>
        </template>
      </div>
    </div>
  </section>
  <p v-else-if="error" class="error">{{ error }}</p>
</template>
