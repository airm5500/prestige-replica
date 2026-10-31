<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref } from 'vue'
import { api, type SiteSummary } from '../api'
import { useSession } from '../stores/session'
import StatusBadge from '../components/StatusBadge.vue'
import { ago, duration, pct, roleLabel } from '../format'

const session = useSession()
const fleet = ref<SiteSummary[]>([])
const error = ref('')
let timer: number | undefined

async function refresh() {
  try { fleet.value = await api.fleet(); error.value = '' } catch (e) { error.value = (e as Error).message }
}
const visible = computed(() => fleet.value.filter((s) => !session.activeSiteId || s.id === session.activeSiteId))
const totals = computed(() => ({
  sites: fleet.value.length,
  critical: fleet.value.filter((s) => s.status === 'CRITICAL').length,
  degraded: fleet.value.filter((s) => s.status === 'DEGRADED').length,
  incidents: fleet.value.reduce((a, s) => a + s.openIncidents, 0),
}))
function thread(v: string | undefined) { return v === 'Yes' ? 'ok' : 'ko' }

onMounted(() => { refresh(); timer = window.setInterval(refresh, 10_000) })
onUnmounted(() => window.clearInterval(timer))
</script>

<template>
  <section>
    <h1>Accueil du parc</h1>
    <p v-if="error" class="error" role="alert">{{ error }}</p>
    <div class="kpis">
      <div class="kpi"><span>{{ totals.sites }}</span>pharmacies</div>
      <div class="kpi critical"><span>{{ totals.critical }}</span>en état critique</div>
      <div class="kpi degraded"><span>{{ totals.degraded }}</span>dégradées</div>
      <div class="kpi"><span>{{ totals.incidents }}</span>incidents ouverts</div>
    </div>

    <p v-if="!fleet.length" class="muted">Aucune pharmacie. Commencez par l'onglet Paramètres pour inventorier un site et ses serveurs.</p>

    <article v-for="s in visible" :key="s.id" class="card site" :style="{ borderLeftColor: s.color }">
      <header>
        <h2>{{ s.name }} <small class="muted">[{{ s.code }}] · {{ s.environment }}</small></h2>
        <StatusBadge :health="s.status" />
        <RouterLink v-if="s.openIncidents" to="/incidents" class="muted">{{ s.openIncidents }} incident(s), dont {{ s.criticalIncidents }} critique(s)</RouterLink>
      </header>

      <div v-for="t in s.topologies" :key="t.id" class="topology">
        <h3>
          {{ t.name }} <small class="muted">mode {{ t.mode }}</small>
          <StatusBadge :health="t.status" />
          <span class="muted">primaire : {{ t.primary ?? '—' }} · RPO estimé : {{ t.rpoSecondsEstimate === null ? 'non mesurable' : duration(t.rpoSecondsEstimate) }} · RTO : non mesuré (Lot 3)</span>
        </h3>
        <div class="table-wrap">
          <table>
            <thead>
              <tr><th>Nœud</th><th>Rôle</th><th>État</th><th>Threads IO / SQL</th><th>Retard</th><th>GTID</th><th>Disque libre</th><th>Dernier contact</th></tr>
            </thead>
            <tbody>
              <tr v-for="n in t.nodes" :key="n.id">
                <td><RouterLink :to="`/nodes/${n.id}`">{{ n.name }}</RouterLink><div class="muted small">{{ n.version ?? '' }}</div></td>
                <td>{{ roleLabel[n.expectedRole] }}</td>
                <td><StatusBadge :health="n.status" /></td>
                <td>
                  <template v-if="n.channels.length">
                    <span v-for="c in n.channels" :key="c.name" class="threads">
                      <span :class="thread(c.ioRunning)">IO {{ c.ioRunning }}</span> /
                      <span :class="thread(c.sqlRunning)">SQL {{ c.sqlRunning }}</span>
                    </span>
                  </template>
                  <span v-else class="muted">—</span>
                </td>
                <td>{{ n.channels.length ? n.channels.map((c) => duration(c.lagSeconds)).join(', ') : '—' }}</td>
                <td class="mono small">{{ n.gtidCurrentPos || '—' }}</td>
                <td>{{ pct(n.minDiskFreePct) }}</td>
                <td>{{ ago(n.lastSeenAt) }}</td>
              </tr>
              <tr v-if="!t.nodes.length"><td colspan="8" class="muted">Aucun nœud dans cette topologie.</td></tr>
            </tbody>
          </table>
        </div>
      </div>
      <p v-if="s.unassignedNodes.length" class="muted">
        Nœuds hors topologie :
        <RouterLink v-for="n in s.unassignedNodes" :key="n.id" :to="`/nodes/${n.id}`" class="chip">{{ n.name }}</RouterLink>
      </p>
    </article>
  </section>
</template>
