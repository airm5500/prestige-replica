<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { api, type BinlogCatalog, type NodeSummary, type PitrPlan, type Topology } from '../api'
import { bytes, date } from '../format'

const props = defineProps<{ topo: Topology; nodes: NodeSummary[]; canOperate: boolean; canPolicy: boolean }>()
const emit = defineEmits<{ changed: [] }>()
const router = useRouter()
const route = useRoute()

const catalog = ref<BinlogCatalog | null>(null)
const error = ref('')
const message = ref('')
const editing = ref<{ binlogArchiveEnabled: boolean; binlogArchiveNodeId: string | null; binlogArchiveMinutes: number } | null>(null)
const form = ref<{ at: string; tables: string; reason: string } | null>(null)
/** Transaction repérée dans l'explorateur : arrêt exact juste avant elle. */
const stopBefore = ref<{ relativePath: string; position: number; gtid: string; at: string } | null>(null)
const plan = ref<PitrPlan | null>(null)
const showFiles = ref(false)
let timer: number | undefined

const nodeName = (id: string | null) => props.nodes.find((n) => n.id === id)?.name ?? '—'
const archiveNode = computed(() => props.topo.binlogArchiveNodeId ?? props.topo.backupNodeId)
const capable = computed(() => props.nodes.filter((n) => n.capabilities.includes('binlogArchive')))
const lagMinutes = computed(() => {
  const u = catalog.value?.coveredUntil
  return u ? Math.max(0, Math.round((Date.now() - Date.parse(u)) / 60000)) : null
})

async function load() {
  try {
    catalog.value = await api.binlogs(props.topo.id)
  } catch (e) { error.value = (e as Error).message }
}
watch(() => props.topo.id, () => { catalog.value = null; editing.value = null; form.value = null; plan.value = null; load() })
onMounted(() => {
  load()
  timer = window.setInterval(load, 15_000)
  // Arrivée depuis l'explorateur des journaux : « Restaurer juste avant » cette transaction.
  const at = route.query.pitr as string | undefined
  if (at && !Number.isNaN(Date.parse(at))) {
    const pos = Number(route.query.pitrPos)
    if (route.query.pitrFile && pos > 0 && route.query.pitrGtid) {
      stopBefore.value = { relativePath: String(route.query.pitrFile), position: pos, gtid: String(route.query.pitrGtid), at: localInput(new Date(at)) }
    }
    form.value = { at: localInput(new Date(at)), tables: String(route.query.pitrTables ?? ''), reason: `Revenir avant la transaction ${route.query.pitrGtid ?? ''}`.trim() }
    router.replace({ query: {} })
  }
})
onUnmounted(() => window.clearInterval(timer))

function edit() {
  editing.value = { binlogArchiveEnabled: props.topo.binlogArchiveEnabled, binlogArchiveNodeId: props.topo.binlogArchiveNodeId ?? props.topo.backupNodeId,
    binlogArchiveMinutes: props.topo.binlogArchiveMinutes }
}
async function save() {
  error.value = ''
  try {
    await api.binlogPolicy(props.topo.id, editing.value!)
    editing.value = null
    message.value = 'Archivage des journaux enregistré'
    emit('changed')
    load()
  } catch (e) { error.value = (e as Error).message }
}

/** Heure locale du navigateur (celle de la pharmacie) au format attendu par datetime-local. */
function localInput(d: Date) {
  const p = (n: number) => String(n).padStart(2, '0')
  return `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())}T${p(d.getHours())}:${p(d.getMinutes())}:${p(d.getSeconds())}`
}
function openForm() {
  form.value = { at: localInput(new Date(Date.now() - 15 * 60_000)), tables: '', reason: '' }
  stopBefore.value = null
  plan.value = null
}
const stopAt = computed(() => (form.value?.at ? new Date(form.value.at).toISOString() : ''))
const tables = computed(() => (form.value?.tables ?? '').split(/[\s,;]+/).map((s) => s.trim()).filter(Boolean))
const tablesValid = computed(() => tables.value.every((t) => /^[A-Za-z0-9_$]{1,64}\.[A-Za-z0-9_$]{1,64}$/.test(t)))

async function check() {
  error.value = ''
  plan.value = null
  try { plan.value = await api.pitrPlan(props.topo.id, stopAt.value) } catch (e) { error.value = (e as Error).message }
}
watch(() => form.value?.at, (v) => {
  plan.value = null
  if (stopBefore.value && v !== stopBefore.value.at) stopBefore.value = null // autre instant choisi à la main
})

async function submit() {
  error.value = ''
  try {
    const op = await api.pitr(props.topo.id, { stopAt: stopAt.value, extract: tables.value, reason: form.value!.reason,
      stopBefore: stopBefore.value ? { relativePath: stopBefore.value.relativePath, position: stopBefore.value.position, gtid: stopBefore.value.gtid } : null })
    form.value = null
    plan.value = null
    router.push({ path: '/operations', query: { op: op.id } })
  } catch (e) { error.value = (e as Error).message }
}
const statusText = { OK: 'OK', WARN: 'Attention', FAIL: 'Bloquant' } as const
</script>

<template>
  <div class="card">
    <h2>Journaux binaires et restauration à un instant</h2>
    <p v-if="message" class="ok" role="status">{{ message }}</p>
    <p v-if="error" class="error" role="alert">{{ error }}</p>

    <div class="grid2">
      <div>
        <dl v-if="!editing" class="kv">
          <dt>Archivage continu</dt>
          <dd>{{ topo.binlogArchiveEnabled ? `toutes les ${topo.binlogArchiveMinutes} min, par ${nodeName(archiveNode)}` : 'désactivé' }}</dd>
          <dt>Couverture</dt>
          <dd v-if="catalog?.coveredFrom">du {{ date(catalog.coveredFrom) }} au {{ date(catalog.coveredUntil) }}
            <span v-if="lagMinutes !== null" :class="lagMinutes > Math.max(15, 3 * topo.binlogArchiveMinutes) ? 'ko' : 'muted'"> (dernière transaction archivée il y a {{ lagMinutes }} min)</span></dd>
          <dd v-else class="muted">aucun journal archivé</dd>
          <dt>Volume archivé</dt><dd>{{ catalog ? `${catalog.files.length} fichier(s), ${bytes(catalog.totalBytes)} chiffrés` : '—' }}</dd>
          <dt>Dernier passage</dt>
          <dd v-if="catalog?.lastRun">{{ date(catalog.lastRun.requestedAt) }} —
            <span :class="catalog.lastRun.status === 'SUCCEEDED' ? 'ok' : catalog.lastRun.status === 'FAILED' ? 'ko' : ''">{{ catalog.lastRun.status === 'SUCCEEDED' ? 'réussi' : catalog.lastRun.status === 'FAILED' ? 'échec' : 'en cours' }}</span>
            <span v-if="catalog.lastRun.error" class="small"> : {{ catalog.lastRun.error }}</span></dd>
          <dd v-else class="muted">—</dd>
        </dl>
        <div v-if="catalog?.gapAt" class="warn-box">
          Trou dans l'archive signalé le {{ date(catalog.gapAt) }} : {{ catalog.gapDetail }}. La restauration à un instant
          reste possible à partir de la prochaine sauvegarde complète.
        </div>
        <div class="toolbar">
          <button v-if="!editing && canPolicy" @click="edit">Régler l'archivage</button>
          <button v-if="!form && canOperate" class="primary" @click="openForm">Restaurer à un instant…</button>
          <button v-if="catalog?.files.length" class="link" @click="showFiles = !showFiles">{{ showFiles ? 'Masquer' : 'Voir' }} les fichiers</button>
        </div>
        <form v-if="editing" class="form" @submit.prevent="save">
          <label class="check"><input v-model="editing.binlogArchiveEnabled" type="checkbox" /> Archiver en continu les journaux binaires du primaire</label>
          <label>Serveur qui archive (son agent lit les journaux du primaire et les dépose, chiffrés, dans le stockage des sauvegardes)
            <select v-model="editing.binlogArchiveNodeId">
              <option v-for="n in nodes" :key="n.id" :value="n.id">{{ n.name }}{{ n.capabilities.includes('binlogArchive') ? '' : ' — non activé dans agent.json' }}</option>
            </select>
          </label>
          <label>Intervalle (minutes) : c'est la perte maximale si le primaire et ses répliques étaient perdus ensemble
            <input v-model.number="editing.binlogArchiveMinutes" type="number" min="1" max="60" />
          </label>
          <p v-if="!capable.length" class="muted small">Aucun agent n'a encore activé « binlogArchive » dans agent.json.</p>
          <div class="toolbar"><button type="submit" class="primary">Enregistrer</button><button type="button" @click="editing = null">Annuler</button></div>
        </form>
      </div>

      <form v-if="form" class="form" @submit.prevent="submit">
        <h3>Restaurer à un instant précis</h3>
        <p class="muted small">La production n'est pas modifiée : la base est reconstruite dans une instance isolée, au plus près de l'instant choisi,
          et les tables demandées en sont extraites (fichiers chiffrés). Exemple : retrouver des ventes supprimées par erreur à 14 h 32 en visant 14 h 31.</p>
        <label>Instant visé (heure de ce poste ; tout ce qui a été validé avant est restauré)
          <input v-model="form.at" type="datetime-local" step="1" required />
        </label>
        <p v-if="stopBefore" class="ok small">Arrêt exact juste avant la transaction <span class="mono">{{ stopBefore.gtid }}</span>
          repérée dans les journaux : les autres transactions de la même seconde sont rejouées.</p>
        <label>Tables à extraire (base.table, séparées par des virgules ; vide = vérification seulement)
          <input v-model="form.tables" placeholder="prestige.vente, prestige.vente_ligne" autocomplete="off" />
        </label>
        <p v-if="!tablesValid" class="error small">Forme attendue : base.table</p>
        <label>Motif (journal d'audit)
          <input v-model="form.reason" required maxlength="300" placeholder="Ventes du 31/10 supprimées par erreur" />
        </label>
        <div class="toolbar"><button type="button" @click="check">Vérifier</button></div>
        <template v-if="plan">
          <table class="checks"><tbody>
            <tr v-for="c in plan.checks" :key="c.id"><td :class="c.status === 'OK' ? 'ok' : c.status === 'FAIL' ? 'ko' : 'warn'">{{ statusText[c.status] }}</td><td>{{ c.message }}</td></tr>
          </tbody></table>
          <p v-if="plan.ready" class="small">Exécuté par <strong>{{ plan.executor }}</strong> : sauvegarde du {{ date(plan.backupAt) }},
            puis {{ plan.binlogs.length }} journal(aux) rejoué(s) jusqu'au {{ date(stopAt) }}.</p>
        </template>
        <div class="toolbar">
          <button type="submit" class="primary" :disabled="!plan?.ready || !tablesValid || !form.reason.trim()">Lancer la restauration isolée</button>
          <button type="button" @click="form = null; plan = null">Annuler</button>
        </div>
      </form>
    </div>

    <div v-if="showFiles && catalog" class="table-wrap"><table>
      <thead><tr><th>Fichier</th><th>Serveur</th><th>Première transaction</th><th>Dernière transaction</th><th>GTID</th><th>Taille</th><th></th></tr></thead>
      <tbody>
        <tr v-for="f in catalog.files" :key="f.id">
          <td class="mono small">{{ f.fileName }}</td><td>{{ f.serverId }}</td>
          <td>{{ date(f.firstEventAt) }}</td><td>{{ date(f.lastEventAt) }}</td>
          <td class="mono small">{{ f.firstGtid ?? '—' }} → {{ f.lastGtid ?? '—' }}</td>
          <td>{{ bytes(f.sizeBytes) }}</td><td class="small muted">{{ f.complete ? '' : 'en cours d\'écriture' }}</td>
        </tr>
      </tbody>
    </table></div>
  </div>
</template>
