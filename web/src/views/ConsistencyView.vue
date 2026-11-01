<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { api, json, type ConsistencyOverview, type ConsistencyPolicy, type Operation, type Topology } from '../api'
import { useSession } from '../stores/session'
import { date, duration, opStatusClass, opStatusLabel, weekdayLabel } from '../format'

interface KeyDiff { key: string; issue: string }
interface ChunkDiff { lower?: string; upper?: string; primaryRows: number; replicaRows: number; keys?: KeyDiff[]; moreKeys?: boolean }
interface TableCheck { table: string; rows: number; chunks: number; status: string; reason?: string; divergences?: ChunkDiff[]; seconds: number }
interface SchemaDiff { kind: string; object: string; issue: string; primary?: string; replica?: string }
interface CheckResult {
  status: string; mode: string; primary: string; databases: string[]; schemaObjects: number; schemaDifferences: SchemaDiff[]
  tables: TableCheck[]; rows: number; completed: boolean; lastTable: string; totalSeconds: number
  summary: { tablesChecked: number; divergentTables: number; inconclusiveTables: number; skippedTables: number; schemaDifferences: number }
}

const session = useSession()
const route = useRoute()
const router = useRouter()
const topologies = ref<Topology[]>([])
const topoId = ref(localStorage.getItem('prm.backupTopo') ?? '')
const overview = ref<ConsistencyOverview | null>(null)
const recent = ref<Operation[]>([])
const selected = ref<Operation | null>(null)
const policy = ref<ConsistencyPolicy | null>(null)
const error = ref('')
const message = ref('')
let timer: number | undefined

const canRun = computed(() => session.can('SUPER_ADMIN', 'SITE_ADMIN', 'OPERATOR'))
const canPolicy = computed(() => session.can('SUPER_ADMIN', 'SITE_ADMIN'))
const topoChoices = computed(() => topologies.value.filter((t) => !session.activeSiteId || t.siteId === session.activeSiteId))
const topo = computed(() => topologies.value.find((t) => t.id === topoId.value) ?? null)
const siteOf = (siteId: string) => session.sites.find((s) => s.id === siteId)
const result = computed(() => json<CheckResult>(selected.value?.result))
const params = computed(() => json<Record<string, any>>(selected.value?.params) ?? {})
const busy = computed(() => !!overview.value?.running)

async function load() {
  try {
    if (!topologies.value.length) topologies.value = await api.topologies()
    if (!topoChoices.value.some((t) => t.id === topoId.value)) topoId.value = topoChoices.value[0]?.id ?? ''
    if (!topoId.value) return
    overview.value = await api.consistency(topoId.value)
    recent.value = await api.operations({ topologyId: topoId.value, type: 'CONSISTENCY_CHECK', limit: 15 })
    const id = (route.query.op as string | undefined) ?? overview.value.replicas.find((r) => r.last)?.last?.id
    if (id && (!selected.value || selected.value.id !== id || ['QUEUED', 'RUNNING'].includes(selected.value.status))) {
      selected.value = (await api.operation(id)).operation
    }
    error.value = ''
  } catch (e) { error.value = (e as Error).message }
}
watch(topoId, (v) => { localStorage.setItem('prm.backupTopo', v); overview.value = null; selected.value = null; policy.value = null; load() })
watch(() => route.query.op, () => { selected.value = null; load() })
onMounted(() => { load(); timer = window.setInterval(() => { if (busy.value) load() }, 3000) })
onUnmounted(() => window.clearInterval(timer))

async function run(nodeId: string, mode: 'FULL' | 'SCHEMA', startAfter?: string) {
  error.value = ''
  message.value = ''
  try {
    const op = await api.consistencyCheck(nodeId, { mode, startAfter: startAfter ?? null })
    message.value = 'Contrôle demandé : il s\'exécute sur la réplique, à débit limité.'
    router.push({ query: { op: op.id } })
    await load()
  } catch (e) { error.value = (e as Error).message }
}

function edit() {
  const t = topo.value!
  policy.value = { consistencyEnabled: t.consistencyEnabled, consistencyWeekday: t.consistencyWeekday, consistencyHour: t.consistencyHour,
    consistencyMode: t.consistencyMode, consistencyDatabases: t.consistencyDatabases ?? '', consistencyExclude: t.consistencyExclude ?? '',
    consistencyMaxAgeDays: t.consistencyMaxAgeDays }
}
async function save() {
  error.value = ''
  try {
    const t = await api.consistencyPolicy(topoId.value, policy.value!)
    topologies.value = topologies.value.map((x) => (x.id === t.id ? t : x))
    policy.value = null
    message.value = 'Réglages enregistrés'
  } catch (e) { error.value = (e as Error).message }
}

const statusText: Record<string, string> = {
  NONE: 'jamais contrôlée', OK: 'identique au primaire', DIVERGENT: 'diffère du primaire', INCONCLUSIVE: 'non concluant',
  FAILED: 'dernier contrôle en échec', REBUILT: 'reconstruite depuis le dernier contrôle', UNKNOWN: '—',
}
const statusClass: Record<string, string> = { OK: 'ok', DIVERGENT: 'ko', INCONCLUSIVE: 'warn', FAILED: 'ko', REBUILT: 'muted', NONE: 'muted' }
const issueText: Record<string, string> = {
  MISSING_ON_REPLICA: 'absente de la réplique', EXTRA_ON_REPLICA: 'en trop sur la réplique', DIFFERENT: 'différente',
}
const tableText: Record<string, string> = { OK: 'identique', DIVERGENT: 'différente', INCONCLUSIVE: 'non concluant', SKIPPED: 'non comparée' }
const kindText: Record<string, string> = {
  TABLE: 'Table', COLUMN: 'Colonne', INDEX: 'Index', VIEW: 'Vue', TRIGGER: 'Déclencheur', ROUTINE: 'Procédure', EVENT: 'Événement', DATABASE: 'Base',
}
const problemTables = computed(() => (result.value?.tables ?? []).filter((t) => t.status !== 'OK'))
const okTables = computed(() => (result.value?.tables ?? []).filter((t) => t.status === 'OK').length)
const nodeOf = (op: Operation) => overview.value?.replicas.find((r) => r.nodeId === op.nodeId)?.node ?? '—'
</script>

<template>
  <section>
    <h1>Cohérence des répliques</h1>
    <p class="muted">Chaque réplique compare son schéma et ses données à ceux du primaire, par tranches de clé primaire et à débit limité.
      Les deux côtés sont lus sans être modifiés. Un écart n'est jamais corrigé automatiquement : la proposition est de reconstruire la
      réplique depuis une sauvegarde vérifiée.</p>
    <div class="toolbar">
      <label>Topologie
        <select v-model="topoId">
          <option v-for="t in topoChoices" :key="t.id" :value="t.id">{{ siteOf(t.siteId)?.name }} [{{ siteOf(t.siteId)?.code }}] — {{ t.name }}</option>
        </select>
      </label>
    </div>
    <p v-if="message" class="ok" role="status">{{ message }}</p>
    <p v-if="error" class="error" role="alert">{{ error }}</p>

    <div class="grid2">
      <div class="card">
        <h2>Répliques</h2>
        <table>
          <thead><tr><th>Réplique</th><th>État</th><th>Dernier contrôle</th><th>Dernier contrôle complet sans écart</th><th></th></tr></thead>
          <tbody>
            <tr v-for="r in overview?.replicas ?? []" :key="r.nodeId">
              <td><RouterLink :to="`/nodes/${r.nodeId}`">{{ r.node }}</RouterLink></td>
              <td :class="statusClass[r.status]">{{ statusText[r.status] ?? r.status }}</td>
              <td class="small"><a v-if="r.last" href="#" @click.prevent="router.push({ query: { op: r.last.id } })">{{ date(r.last.finishedAt) }}</a><span v-else>—</span></td>
              <td class="small">{{ r.lastOkAt ? date(r.lastOkAt) : '—' }}</td>
              <td class="actions">
                <template v-if="canRun">
                  <button :disabled="busy" @click="run(r.nodeId, 'FULL')">Contrôler</button>
                  <button :disabled="busy" class="link" @click="run(r.nodeId, 'SCHEMA')">Schéma seul</button>
                </template>
              </td>
            </tr>
          </tbody>
        </table>
        <p v-if="overview?.running" class="small">Contrôle en cours sur {{ nodeOf(overview.running) }} depuis {{ date(overview.running.startedAt ?? overview.running.requestedAt) }}.</p>
        <p v-if="overview?.replicas.some((r) => r.status === 'DIVERGENT')" class="warn-box">
          Une réplique qui diffère du primaire n'est plus une copie fidèle : ne pas basculer vers elle sans vérification (les plans de bascule
          l'indiquent). Reconstruisez-la depuis une sauvegarde vérifiée (page Sauvegardes, « Reconstruire une réplique »), puis relancez un contrôle.
        </p>
      </div>

      <div class="card">
        <h2>Contrôle planifié</h2>
        <dl v-if="!policy && topo" class="kv">
          <dt>Planification</dt>
          <dd>{{ topo.consistencyEnabled ? `chaque ${weekdayLabel[topo.consistencyWeekday]} à ${topo.consistencyHour} h (heure de la pharmacie), ${topo.consistencyMode === 'FULL' ? 'schéma et données' : 'schéma seul'}` : 'désactivée' }}</dd>
          <dt>Bases</dt><dd>{{ topo.consistencyDatabases || 'toutes (hors bases système)' }}</dd>
          <dt>Exclusions</dt><dd>{{ topo.consistencyExclude || 'aucune' }}</dd>
          <dt>Alerte si aucun contrôle complet sans écart depuis</dt><dd>{{ topo.consistencyMaxAgeDays ? `${topo.consistencyMaxAgeDays} jours` : 'jamais' }}</dd>
        </dl>
        <div class="toolbar"><button v-if="!policy && canPolicy && topo" @click="edit">Modifier</button></div>
        <form v-if="policy" class="form" @submit.prevent="save">
          <label class="check"><input v-model="policy.consistencyEnabled" type="checkbox" /> Contrôler chaque semaine toutes les répliques</label>
          <div class="grid2">
            <label>Jour <select v-model.number="policy.consistencyWeekday"><option v-for="d in 7" :key="d" :value="d">{{ weekdayLabel[d] }}</option></select></label>
            <label>Heure (pharmacie) <input v-model.number="policy.consistencyHour" type="number" min="0" max="23" /></label>
          </div>
          <label>Étendue
            <select v-model="policy.consistencyMode"><option value="FULL">Schéma et données</option><option value="SCHEMA">Schéma seul</option></select>
          </label>
          <label>Bases à comparer (séparées par des virgules ; vide = toutes) <input v-model="policy.consistencyDatabases" placeholder="prestige" autocomplete="off" /></label>
          <label>Exclusions (base.table ou base.*) <input v-model="policy.consistencyExclude" placeholder="prestige.journal_connexions, archives.*" autocomplete="off" /></label>
          <label>Alerte si aucun contrôle complet sans écart depuis (jours ; 0 = jamais)
            <input v-model.number="policy.consistencyMaxAgeDays" type="number" min="0" max="365" />
          </label>
          <div class="toolbar"><button type="submit" class="primary">Enregistrer</button><button type="button" @click="policy = null">Annuler</button></div>
        </form>
      </div>
    </div>

    <div v-if="selected" class="card">
      <h2>Contrôle de {{ nodeOf(selected) }} du {{ date(selected.requestedAt) }}
        <span :class="['badge', opStatusClass[selected.status]]">{{ opStatusLabel[selected.status] }}</span></h2>
      <p v-if="['QUEUED', 'RUNNING'].includes(selected.status)" class="muted">En cours… <RouterLink :to="{ path: '/operations', query: { op: selected.id } }">voir le journal de l'opération</RouterLink></p>
      <p v-else-if="selected.error" class="error">{{ selected.error }}</p>
      <template v-if="result">
        <p :class="statusClass[result.status]"><strong>{{ result.status === 'OK' ? 'Aucun écart : la réplique est identique au primaire.'
          : result.status === 'DIVERGENT' ? 'Écarts trouvés : la réplique n\'est plus une copie fidèle du primaire.'
          : result.completed ? 'Contrôle non concluant : certaines tranches changeaient trop vite pour être comparées.'
          : 'Contrôle interrompu (durée maximale atteinte) : à reprendre.' }}</strong></p>
        <dl class="kv">
          <dt>Primaire comparé</dt><dd class="mono">{{ result.primary }}</dd>
          <dt>Étendue</dt><dd>{{ result.mode === 'FULL' ? 'schéma et données' : 'schéma seul' }} — {{ result.databases?.join(', ') }}<span v-if="params.startAfter">, reprise après {{ params.startAfter }}</span></dd>
          <dt>Objets de schéma</dt><dd>{{ result.schemaObjects }} comparés, {{ result.summary.schemaDifferences }} différence(s)</dd>
          <dt v-if="result.mode === 'FULL'">Données</dt>
          <dd v-if="result.mode === 'FULL'">{{ result.summary.tablesChecked }} table(s), {{ result.rows.toLocaleString('fr-FR') }} lignes ; {{ result.summary.divergentTables }} différente(s),
            {{ result.summary.inconclusiveTables }} non concluante(s), {{ result.summary.skippedTables }} non comparée(s)</dd>
          <dt>Durée</dt><dd>{{ duration(result.totalSeconds) }}</dd>
        </dl>
        <div v-if="!result.completed && canRun" class="toolbar">
          <button class="primary" :disabled="busy" @click="run(selected.nodeId!, 'FULL', result.lastTable)">Reprendre après {{ result.lastTable }}</button>
        </div>

        <template v-if="result.schemaDifferences?.length">
          <h3>Différences de schéma</h3>
          <div class="table-wrap"><table>
            <thead><tr><th>Objet</th><th></th><th>Écart</th><th>Primaire</th><th>Réplique</th></tr></thead>
            <tbody>
              <tr v-for="(d, i) in result.schemaDifferences" :key="i">
                <td>{{ kindText[d.kind.toUpperCase()] ?? d.kind }}</td><td class="mono small">{{ d.object }}</td><td>{{ issueText[d.issue] ?? d.issue }}</td>
                <td class="mono small">{{ d.primary ?? '' }}</td><td class="mono small">{{ d.replica ?? '' }}</td>
              </tr>
            </tbody>
          </table></div>
        </template>

        <template v-if="problemTables.length">
          <h3>Tables</h3>
          <p class="muted small">{{ okTables }} table(s) identique(s) non listée(s). Pour une table différente, les clés primaires des lignes en écart
            sont données (au plus 50 par tranche) : comparez-les sur les deux serveurs avant toute décision.</p>
          <div v-for="t in problemTables" :key="t.table" class="table-block">
            <p><span class="mono">{{ t.table }}</span> — <span :class="statusClass[t.status] ?? 'muted'">{{ tableText[t.status] ?? t.status }}</span>
              <span v-if="t.reason" class="small muted"> : {{ t.reason }}</span></p>
            <table v-if="t.divergences?.length" class="small">
              <thead><tr><th>Tranche (clé)</th><th>Lignes primaire / réplique</th><th>Lignes en écart</th></tr></thead>
              <tbody>
                <tr v-for="(c, i) in t.divergences" :key="i">
                  <td class="mono">{{ c.lower ?? 'début' }} → {{ c.upper ?? 'fin' }}</td>
                  <td>{{ c.primaryRows }} / {{ c.replicaRows }}</td>
                  <td><span v-for="k in c.keys ?? []" :key="k.key" class="keydiff"><span class="mono">{{ k.key }}</span> {{ issueText[k.issue] ?? k.issue }}</span>
                    <span v-if="c.moreKeys" class="muted"> (et d'autres)</span></td>
                </tr>
              </tbody>
            </table>
          </div>
        </template>
      </template>
    </div>

    <div class="card">
      <h2>Historique</h2>
      <table>
        <thead><tr><th>Demandé</th><th>Réplique</th><th>Par</th><th>Résumé</th><th>État</th><th>Résultat</th></tr></thead>
        <tbody>
          <tr v-for="o in recent" :key="o.id" class="clickable" :class="o.id === selected?.id ? 'selected' : ''" @click="router.push({ query: { op: o.id } })">
            <td class="small">{{ date(o.requestedAt) }}</td><td>{{ nodeOf(o) }}</td><td class="small">{{ o.requestedBy }}</td>
            <td class="small">{{ o.summary }}</td>
            <td><span :class="['badge', opStatusClass[o.status]]">{{ opStatusLabel[o.status] }}</span></td>
            <td :class="statusClass[json<CheckResult>(o.result)?.status ?? ''] ?? ''">{{ statusText[json<CheckResult>(o.result)?.status ?? ''] ?? '' }}</td>
          </tr>
        </tbody>
      </table>
    </div>
  </section>
</template>

<style scoped>
.keydiff { display: inline-block; margin-right: .8rem; }
.table-block { margin-bottom: .8rem; }
td.actions { white-space: nowrap; }
</style>
