<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { api, type NodeSummary, type SiteSummary, type SwitchoverPlan, type FailoverPlan, type Topology } from '../api'
import { useSession } from '../stores/session'
import { duration, roleLabel } from '../format'

const session = useSession()
const router = useRouter()
const route = useRoute()
const mode = ref<'planned' | 'urgent'>(route.query.mode === 'urgent' ? 'urgent' : 'planned')
const fplan = ref<FailoverPlan | null>(null)
const fenced = ref(false)
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
const reset = () => { plan.value = null; fplan.value = null; fenced.value = false; confirmation.value = '' }
watch(topoId, () => { targetId.value = mode.value === 'urgent' ? '' : candidates.value[0]?.id ?? ''; reset() })
watch(targetId, reset)
watch(mode, (m) => { targetId.value = m === 'urgent' ? '' : candidates.value[0]?.id ?? ''; reset(); router.replace({ query: m === 'urgent' ? { mode: 'urgent' } : {} }) })
watch(() => session.activeSiteId, load)
onMounted(load)

async function check() {
  error.value = ''; busy.value = true
  try {
    await load()
    if (mode.value === 'urgent') fplan.value = await api.failoverPlan(topoId.value, targetId.value || null)
    else plan.value = await api.switchoverPlan(topoId.value, targetId.value)
  } catch (e) { error.value = (e as Error).message }
  busy.value = false
}
async function submit() {
  error.value = ''
  try {
    const op = mode.value === 'urgent'
      ? await api.failover(topoId.value, { targetNodeId: fplan.value?.targetId ?? null, fencingConfirmed: fenced.value, topologyConfirmation: confirmation.value })
      : await api.switchover(topoId.value, { targetNodeId: targetId.value, reattachOldPrimary: reattach.value, topologyConfirmation: confirmation.value })
    router.push({ path: '/operations', query: { op: op.id } })
  } catch (e) { error.value = (e as Error).message }
}
const canAdmin = computed(() => session.can('SUPER_ADMIN', 'SITE_ADMIN'))
const manualId = ref('')
const manualConfirm = ref('')
const manualDone = ref('')
async function declareManual() {
  error.value = ''; manualDone.value = ''
  try {
    await api.declareManualFailover(topoId.value, { newPrimaryNodeId: manualId.value, topologyConfirmation: manualConfirm.value })
    manualDone.value = 'Inventaire mis à jour : reconstruisez l\'ancien primaire depuis une sauvegarde.'
    manualConfirm.value = ''; await load()
  } catch (e) { error.value = (e as Error).message }
}
const lag = (n: NodeSummary) => n.channels[0]?.lagSeconds ?? null
</script>

<template>
  <section>
    <h1>Bascule du primaire</h1>
    <div class="modes" role="tablist">
      <button :class="{ on: mode === 'planned' }" role="tab" @click="mode = 'planned'">Planifiée (primaire en service)</button>
      <button :class="{ on: mode === 'urgent', urgent: true }" role="tab" @click="mode = 'urgent'">Urgence (primaire perdu)</button>
    </div>
    <p v-if="mode === 'urgent'" class="muted">À utiliser seulement si le serveur principal est en panne. La réplique la plus avancée reprend le service
      après avoir appliqué tout ce qu'elle a reçu. Les ventes du primaire qu'elle n'a pas reçues sont perdues (la perte estimée est affichée avant validation).
      L'ancien primaire est d'abord isolé pour qu'il ne puisse plus jamais recevoir de ventes, puis devra être reconstruit. Une seconde personne doit approuver.
      Si la console elle-même est inaccessible : <a href="https://github.com/airm5500/prestige-replica/blob/main/docs/procedure-urgence.md" target="_blank">procédure manuelle</a>.</p>
    <p v-else class="muted">Échange des rôles entre le primaire et une réplique à jour, par exemple pour une maintenance du serveur principal.
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
            <select v-model="targetId" :required="mode === 'planned'">
              <option v-if="mode === 'urgent'" value="">La plus avancée (recommandé)</option>
              <option v-for="n in candidates" :key="n.id" :value="n.id">{{ n.name }} ({{ n.addressLabel ?? 'adresse à renseigner' }})</option>
            </select>
          </label>
          <button type="submit" :disabled="(mode === 'planned' && !targetId) || busy">Lancer les contrôles préalables</button>
        </form>
      </div>

      <div v-if="fplan" class="card">
        <h2>Contrôles — {{ fplan.ready ? 'bascule d\'urgence possible' : 'bascule d\'urgence impossible' }}</h2>
        <table class="checks">
          <tbody>
            <tr v-for="c in fplan.checks" :key="c.id">
              <td :class="c.status === 'OK' ? 'ok' : c.status === 'WARN' ? 'warn' : 'ko'">{{ c.status }}</td><td>{{ c.message }}</td>
            </tr>
          </tbody>
        </table>
        <form v-if="fplan.ready" class="form" @submit.prevent="submit">
          <h3>Déroulement</h3>
          <ol class="small">
            <li v-if="fplan.oldAgentAlive">L'agent de {{ fplan.oldPrimary }} le passe en lecture seule, ou arrête son service MariaDB s'il ne répond plus.</li>
            <li v-else>{{ fplan.oldPrimary }} est injoignable : vous confirmez ci-dessous l'avoir éteint ou débranché.</li>
            <li>{{ fplan.target }} applique tout ce qu'elle a reçu, puis devient primaire.</li>
            <li v-if="fplan.repointed.length">{{ fplan.repointed.join(', ') }} repart{{ fplan.repointed.length > 1 ? 'ent' : '' }} de {{ fplan.target }}.</li>
            <li>Si Prestige passe par le routeur de connexion, il suit automatiquement ; sinon, faites-le pointer vers {{ fplan.target }}.</li>
            <li>{{ fplan.oldPrimary }} reste isolé jusqu'à sa reconstruction depuis une sauvegarde.</li>
          </ol>
          <div class="fence-box"><strong>Perte estimée :</strong> {{ fplan.lossEstimate }}</div>
          <label v-if="fplan.manualFencingRequired" class="check"><input v-model="fenced" type="checkbox" />
            Je confirme que {{ fplan.oldPrimary }} est éteint ou débranché du réseau et ne sera pas rallumé avant sa reconstruction.</label>
          <div class="warn-box">Pharmacie <strong>{{ site?.name }}</strong> [{{ site?.code }}] — primaire perdu {{ fplan.oldPrimary }}.</div>
          <label>Pour confirmer, saisissez le nom de la topologie « {{ topo.name }} »
            <input v-model="confirmation" autocomplete="off" required />
          </label>
          <button type="submit" class="danger" :disabled="confirmation !== topo.name || (fplan.manualFencingRequired && !fenced)">Demander l'approbation de la bascule d'urgence</button>
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
            <li><strong>Si Prestige ne passe pas par le routeur de connexion, faites-le ensuite pointer vers {{ plan.newSource }}.</strong></li>
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

    <details v-if="topo && mode === 'urgent' && canAdmin" class="card">
      <summary>Bascule déjà faite à la main, console injoignable à ce moment-là</summary>
      <p class="small muted">Après la procédure manuelle (<code>prm-agent fence</code> puis <code>prm-agent promote</code>), déclarez ici le serveur promu :
        l'inventaire est aligné et l'ancien primaire est marqué isolé, à reconstruire. Le serveur doit déjà se présenter comme primaire dans ses rapports.</p>
      <form class="form" @submit.prevent="declareManual">
        <label>Serveur promu
          <select v-model="manualId" required>
            <option v-for="n in candidates" :key="n.id" :value="n.id">{{ n.name }}</option>
          </select>
        </label>
        <label>Nom de la topologie « {{ topo.name }} » <input v-model="manualConfirm" autocomplete="off" required /></label>
        <button type="submit" :disabled="manualConfirm !== topo.name">Déclarer le nouveau primaire</button>
        <p v-if="manualDone" class="ok" role="status">{{ manualDone }}</p>
      </form>
    </details>
  </section>
</template>
