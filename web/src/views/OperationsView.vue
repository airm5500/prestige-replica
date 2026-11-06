<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { api, type Operation } from '../api'
import { useSession } from '../stores/session'
import OperationPanel from '../components/OperationPanel.vue'
import { date, opStatusClass, opStatusLabel, opTypeLabel } from '../format'

const session = useSession()
const route = useRoute()
const router = useRouter()
const ops = ref<Operation[]>([])
const filter = ref<'active' | ''>('')
const error = ref('')
let timer: number | undefined

const selected = computed(() => (route.query.op as string | undefined) ?? '')
// Les étapes d'une bascule sont affichées dans le détail de l'opération parente.
const visible = computed(() => ops.value.filter((o) => !o.parentId && (!session.activeSiteId || o.siteId === session.activeSiteId)))
const awaiting = computed(() => visible.value.filter((o) => o.status === 'AWAITING_APPROVAL').length)
const siteCode = (id: string) => session.sites.find((s) => s.id === id)?.code ?? '?'

async function load() {
  try { ops.value = await api.operations({ status: filter.value, limit: 200 }); error.value = '' } catch (e) { error.value = (e as Error).message }
}
const select = (id: string) => router.replace({ query: { op: id } })
onMounted(() => { load(); timer = window.setInterval(load, 10_000) })
onUnmounted(() => window.clearInterval(timer))
</script>

<template>
  <section>
    <h1>Opérations</h1>
    <p class="muted">Chaque action sur MariaDB passe par ici. L'agent n'exécute que des opérations d'une liste fermée ;
      la construction d'une réplique exige l'approbation d'une seconde personne.</p>
    <div class="toolbar">
      <label>Afficher
        <select v-model="filter" @change="load">
          <option value="">Toutes (200 dernières)</option>
          <option value="active">En attente ou en cours</option>
        </select>
      </label>
      <span v-if="awaiting" class="badge degraded">{{ awaiting }} en attente d'approbation</span>
    </div>
    <p v-if="error" class="error" role="alert">{{ error }}</p>
    <div class="split">
      <div class="table-wrap"><table>
        <thead><tr><th>Demandée</th><th>Pharmacie</th><th>Opération</th><th>État</th><th>Par</th></tr></thead>
        <tbody>
          <tr v-for="o in visible" :key="o.id" class="clickable" :class="{ selected: o.id === selected }" tabindex="0"
              @click="select(o.id)" @keydown.enter="select(o.id)">
            <td class="small">{{ date(o.requestedAt) }}</td>
            <td>{{ siteCode(o.siteId) }}</td>
            <td>{{ opTypeLabel[o.type] }}<br /><span class="muted small">{{ o.summary }}</span></td>
            <td><span class="badge" :class="opStatusClass[o.status]">{{ opStatusLabel[o.status] }}</span></td>
            <td class="small">{{ o.requestedBy }}</td>
          </tr>
          <tr v-if="!visible.length"><td colspan="5" class="muted">Aucune opération.</td></tr>
        </tbody>
      </table></div>
      <OperationPanel v-if="selected" :id="selected" @changed="load" />
    </div>
  </section>
</template>
