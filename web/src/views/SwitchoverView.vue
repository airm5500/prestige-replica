<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue'
import { useRouter } from 'vue-router'
import { api, type NodeSummary, type SiteSummary, type SwitchoverPlan, type Topology } from '../api'
import { useSession } from '../stores/session'
import { duration, roleLabel } from '../format'

const session = useSession()
const router = useRouter()
const fleet = ref<SiteSummary[]>([])
const topologies = ref<Topology[]>([])
const topoId = ref('')
const targetId = ref('')
const plan = ref<SwitchoverPlan | null>(null)
const reattach = ref(true)
const confirmation = ref('')
const error = ref('')
const busy = ref(false)

const canOperate = computed(() => session.can('SUPER_ADMIN', 'SITE_ADMIN', 'OPERATOR'))
const topoChoices = computed(() => topologies.value.filter((t) => !session.activeSiteId || t.siteId === session.activeSiteId))
const topo = computed(() => topologies.value.find((t) => t.id === topoId.value) ?? null)
const site = computed(() => session.sites.find((s) => s.id === topo.value?.siteId))
const nodes = computed<NodeSummary[]>(() => fleet.value.flatMap((s) => s.topologies).find((t) => t.id === topoId.value)?.nodes ?? [])
const primary = computed(() => nodes.value.find((n) => n.expectedRole === 'PRIMARY') ?? null)
const candidates = computed(() => nodes.value.filter((n) => n.expectedRole === 'REPLICA'))

async function load() {
  try {
    ;[fleet.value, topologies.value] = await Promise.all([api.fleet(), api.topologies()])
    if (!topoChoices.value.some((t) => t.id === topoId.value)) topoId.value = topoChoices.value[0]?.id ?? ''
  } catch (e) { error.value = (e as Error).message }
}
watch(topoId, () => { targetId.value = candidates.value[0]?.id ?? ''; plan.value = null; confirmation.value = '' })
watch(targetId, () => { plan.value = null })
watch(() => session.activeSiteId, load)
onMounted(load)

async function check() {
  error.value = ''; busy.value = true
  try { await load(); plan.value = await api.switchoverPlan(topoId.value, targetId.value) } catch (e) { error.value = (e as Error).message }
  busy.value = false
}
async function submit() {
  error.value = ''
  try {
    const op = await api.switchover(topoId.value, { targetNodeId: targetId.value, reattachOldPrimary: reattach.value, topologyConfirmation: confirmation.value })
    router.push({ path: '/operations', query: { op: op.id } })
  } catch (e) { error.value = (e as Error).message }
}
const lag = (n: NodeSummary) => n.channels[0]?.lagSeconds ?? null
</script>

<template>
  <section>
    <h1>Bascule planifiée du primaire</h1>
    <p class="muted">Échange des rôles entre le primaire et une réplique à jour, par exemple pour une maintenance du serveur principal.
      Aucune transaction confirmée n'est perdue : la réplique n'est ouverte en écriture qu'après avoir appliqué la dernière transaction de l'ancien primaire.
      Une seconde personne doit approuver.</p>
    <div class="toolbar">
      <label>Topologie
        <select v-model="topoId">
          <option v-for="t in topoChoices" :key="t.id" :value="t.id">{{ session.sites.find((s) => s.id === t.siteId)?.name }} — {{ t.name }}</option>
        </select>
      </label>
    </div>
    <p v-if="error" class="error" role="alert">{{ error }}</p>

    <div v-if="topo" class="grid2">
      <div class="card">
        <h2>Serveurs</h2>
        <table>
          <thead><tr><th>Serveur</th><th>Rôle</th><th>Adresse</th><th>Retard</th></tr></thead>
          <tbody>
            <tr v-for="n in nodes" :key="n.id">
              <td>{{ n.name }}</td><td>{{ roleLabel[n.expectedRole] }}</td><td class="mono small">{{ n.addressLabel ?? '—' }}</td>
              <td>{{ n.expectedRole === 'PRIMARY' ? '—' : duration(lag(n)) }}</td>
            </tr>
          </tbody>
        </table>
        <form v-if="canOperate" class="form" @submit.prevent="check">
          <label>Nouveau primaire
            <select v-model="targetId" required>
              <option v-for="n in candidates" :key="n.id" :value="n.id">{{ n.name }} ({{ n.addressLabel ?? 'adresse à renseigner' }})</option>
            </select>
          </label>
          <button type="submit" :disabled="!targetId || busy">Lancer les contrôles préalables</button>
        </form>
      </div>

      <div v-if="plan" class="card">
        <h2>Contrôles préalables — {{ plan.ready ? 'prêt' : 'bascule impossible' }}</h2>
        <table class="checks">
          <tbody>
            <tr v-for="c in plan.checks" :key="c.id">
              <td :class="c.status === 'OK' ? 'ok' : c.status === 'WARN' ? 'warn' : 'ko'">{{ c.status }}</td><td>{{ c.message }}</td>
            </tr>
          </tbody>
        </table>
        <form v-if="plan.ready" class="form" @submit.prevent="submit">
          <h3>Déroulement</h3>
          <ol class="small">
            <li>{{ plan.oldPrimary }} passe en lecture seule ; les connexions de Prestige sont coupées (quelques secondes d'interruption).</li>
            <li>{{ plan.target }} applique la dernière transaction de {{ plan.oldPrimary }}, puis devient primaire.</li>
            <li v-if="plan.repointed.length">{{ plan.repointed.join(', ') }} repart{{ plan.repointed.length > 1 ? 'ent' : '' }} de {{ plan.target }}.</li>
            <li><strong>Vous devez ensuite faire pointer Prestige vers {{ plan.newSource }}.</strong></li>
          </ol>
          <label class="check"><input v-model="reattach" type="checkbox" />
            Rattacher immédiatement {{ plan.oldPrimary }} comme réplique de {{ plan.target }}
            <span class="muted small">(sinon il reste isolé en lecture seule jusqu'à validation)</span></label>
          <div class="warn-box">Pharmacie <strong>{{ site?.name }}</strong> [{{ site?.code }}] — primaire actuel {{ primary?.name }}.</div>
          <label>Pour confirmer, saisissez le nom de la topologie « {{ topo.name }} »
            <input v-model="confirmation" autocomplete="off" required />
          </label>
          <button type="submit" class="danger" :disabled="confirmation !== topo.name">Demander l'approbation de la bascule</button>
        </form>
      </div>
    </div>
  </section>
</template>
