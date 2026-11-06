<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref, watch } from 'vue'
import { api, type Incident } from '../api'
import { useSession } from '../stores/session'
import StatusBadge from '../components/StatusBadge.vue'
import { date } from '../format'

const session = useSession()
const scope = ref<'open' | 'all'>('open')
const items = ref<Incident[]>([])
const error = ref('')
let timer: number | undefined

const siteName = (id: string) => session.sites.find((s) => s.id === id)?.name ?? id
const visible = computed(() => items.value.filter((i) => !session.activeSiteId || i.siteId === session.activeSiteId))
const canAck = computed(() => session.can('SUPER_ADMIN', 'OPERATOR', 'SITE_ADMIN'))

async function load() {
  try { items.value = await api.incidents(scope.value); error.value = '' } catch (e) { error.value = (e as Error).message }
}
async function ack(i: Incident) {
  const who = window.prompt('Responsable de cet incident :', session.me?.username ?? '')
  if (who === null) return
  try { await api.acknowledge(i.id, who); await load() } catch (e) { error.value = (e as Error).message }
}
watch(scope, load)
onMounted(() => { load(); timer = window.setInterval(load, 15_000) })
onUnmounted(() => window.clearInterval(timer))
</script>

<template>
  <section>
    <h1>Incidents</h1>
    <div class="toolbar">
      <label><input type="radio" value="open" v-model="scope" /> Ouverts</label>
      <label><input type="radio" value="all" v-model="scope" /> Tous (200 derniers)</label>
    </div>
    <p v-if="error" class="error" role="alert">{{ error }}</p>
    <div class="table-wrap"><table>
      <thead><tr><th>Niveau</th><th>Pharmacie</th><th>Incident</th><th>Début</th><th>Dernière observation</th><th>Statut</th><th>Responsable</th><th></th></tr></thead>
      <tbody>
        <tr v-for="i in visible" :key="i.id">
          <td><StatusBadge :severity="i.severity" /></td>
          <td>{{ siteName(i.siteId) }}</td>
          <td>
            <RouterLink v-if="i.nodeId" :to="`/nodes/${i.nodeId}`">{{ i.message }}</RouterLink><span v-else>{{ i.message }}</span>
            <div class="muted small mono">{{ i.type }}</div>
            <details v-if="i.details"><summary class="small">Détail</summary><pre class="details">{{ i.details }}</pre></details>
          </td>
          <td>{{ date(i.openedAt) }}</td>
          <td>{{ date(i.lastSeenAt) }}</td>
          <td>{{ i.status === 'OPEN' ? 'Ouvert' : i.status === 'ACKNOWLEDGED' ? 'Acquitté' : 'Résolu le ' + date(i.resolvedAt) }}</td>
          <td>{{ i.assignee ?? '—' }}<div v-if="i.acknowledgedBy" class="muted small">par {{ i.acknowledgedBy }}, {{ date(i.acknowledgedAt) }}</div></td>
          <td><button v-if="canAck && i.status === 'OPEN'" @click="ack(i)">Acquitter</button></td>
        </tr>
        <tr v-if="!visible.length"><td colspan="8" class="muted">Aucun incident.</td></tr>
      </tbody>
    </table></div>
  </section>
</template>
