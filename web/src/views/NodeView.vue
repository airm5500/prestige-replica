<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref, watch } from 'vue'
import { api, type Incident, type NodeMetric, type NodeSummary, type Site, type Topology } from '../api'
import StatusBadge from '../components/StatusBadge.vue'
import Sparkline from '../components/Sparkline.vue'
import { ago, bytes, date, duration, pct, roleLabel } from '../format'

const props = defineProps<{ id: string }>()
const summary = ref<NodeSummary | null>(null)
const report = ref<Record<string, any> | null>(null)
const incidents = ref<Incident[]>([])
const site = ref<Site | null>(null)
const topology = ref<Topology | null>(null)
const metrics = ref<NodeMetric[]>([])
const hours = ref(24)
const error = ref('')
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
    error.value = ''
  } catch (e) { error.value = (e as Error).message }
}
watch(hours, load)
onMounted(() => { load(); timer = window.setInterval(load, 15_000) })
onUnmounted(() => window.clearInterval(timer))

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
