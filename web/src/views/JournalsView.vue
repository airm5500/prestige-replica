<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { api, json, type Operation, type OperationView, type Topology } from '../api'
import { useSession } from '../stores/session'
import { bytes, date, duration, opStatusClass, opStatusLabel } from '../format'

interface Row { before?: string[]; after?: string[] }
interface Change { type: string; database?: string; table?: string; statement?: string; rows: number; sample?: Row[] }
interface Tx { gtid: string; at: string; serverId: number; file: string; relativePath?: string; pos: number; queries?: string[]; changes: Change[] }
interface SearchResult {
  transactions: Tx[]; total: number; rows: number; events: number; truncated: boolean; files: number; tool: string
  result: { relativePath: string; sha256: string; sizeBytes: number }; totalSeconds: number; masked?: boolean; purged?: boolean
}

const session = useSession()
const route = useRoute()
const router = useRouter()
const topologies = ref<Topology[]>([])
const topoId = ref(localStorage.getItem('prm.backupTopo') ?? '')
const recent = ref<Operation[]>([])
const view = ref<OperationView | null>(null)
const error = ref('')
const open = ref<Record<string, boolean>>({})
let timer: number | undefined

const canSearch = computed(() => session.can('SUPER_ADMIN', 'SITE_ADMIN', 'OPERATOR'))
const topoChoices = computed(() => topologies.value.filter((t) => !session.activeSiteId || t.siteId === session.activeSiteId))
const siteOf = (siteId: string) => session.sites.find((s) => s.id === siteId)

function localInput(d: Date) {
  const p = (n: number) => String(n).padStart(2, '0')
  return `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())}T${p(d.getHours())}:${p(d.getMinutes())}`
}
const form = ref({
  from: localInput(new Date(Date.now() - 24 * 3600_000)), to: localInput(new Date(Date.now() + 60_000)),
  tables: '', databases: '', text: '', reason: '', types: { INSERT: false, UPDATE: false, DELETE: true, DDL: false } as Record<string, boolean>,
})
const split = (s: string) => s.split(/[\s,;]+/).map((x) => x.trim()).filter(Boolean)
const tablesOk = computed(() => split(form.value.tables).every((t) => /^[A-Za-z0-9_$]{1,64}\.[A-Za-z0-9_$]{1,64}$/.test(t)))

const result = computed(() => json<SearchResult>(view.value?.operation.result))
const running = computed(() => view.value && ['QUEUED', 'RUNNING'].includes(view.value.operation.status))
const params = computed(() => json<Record<string, any>>(view.value?.operation.params) ?? {})

async function load() {
  try {
    if (!topologies.value.length) topologies.value = await api.topologies()
    if (!topoChoices.value.some((t) => t.id === topoId.value)) topoId.value = topoChoices.value[0]?.id ?? ''
    if (topoId.value) recent.value = await api.operations({ topologyId: topoId.value, type: 'BINLOG_SEARCH', limit: 10 })
    const id = route.query.op as string | undefined
    if (id && (!view.value || view.value.operation.id !== id || running.value)) view.value = await api.operation(id)
    error.value = ''
  } catch (e) { error.value = (e as Error).message }
}
watch(topoId, (v) => { localStorage.setItem('prm.backupTopo', v); load() })
watch(() => route.query.op, () => { view.value = null; open.value = {}; load() })
onMounted(() => { load(); timer = window.setInterval(() => { if (running.value || !recent.value.length) load() }, 2000) })
onUnmounted(() => window.clearInterval(timer))

async function search() {
  error.value = ''
  try {
    const f = form.value
    const op = await api.binlogSearch(topoId.value, {
      from: new Date(f.from).toISOString(), to: new Date(f.to).toISOString(), databases: split(f.databases), tables: split(f.tables),
      types: Object.entries(f.types).filter(([, v]) => v).map(([k]) => k), text: f.text.trim(), reason: f.reason.trim(),
    })
    router.push({ query: { op: op.id } })
  } catch (e) { error.value = (e as Error).message }
}

function restoreBefore(tx: Tx) {
  const tables = [...new Set(tx.changes.filter((c) => c.table).map((c) => `${c.database}.${c.table}`))].join(', ')
  router.push({ path: '/backups', query: { pitr: tx.at, pitrGtid: tx.gtid, pitrTables: tables, pitrFile: tx.relativePath ?? '', pitrPos: String(tx.pos) } })
}
const typeLabel: Record<string, string> = { INSERT: 'Ajout', UPDATE: 'Modification', DELETE: 'Suppression', DDL: 'Structure / autre' }
const typeClass: Record<string, string> = { INSERT: 'ok', UPDATE: 'warn', DELETE: 'ko', DDL: 'muted' }
/** Une ligne par enregistrement ; pour une modification, « avant → après » dans les colonnes changées. */
function grid(c: Change) {
  const rows = c.sample ?? []
  const n = Math.max(0, ...rows.map((r) => Math.max(r.before?.length ?? 0, r.after?.length ?? 0)))
  const val = (x: string | undefined) => (x === undefined ? '' : x.slice(x.indexOf('=') + 1))
  const cols = Array.from({ length: n }, (_, i) => `@${i + 1}`)
  const lines = rows.map((r) => cols.map((_, i) => {
    const b = val(r.before?.[i]), a = val(r.after?.[i])
    if (c.type === 'DELETE') return { text: b, changed: false }
    if (c.type === 'INSERT' || !r.before) return { text: a, changed: false }
    return b === a ? { text: a, changed: false } : { text: `${b} → ${a}`, changed: true }
  }))
  return { cols, lines }
}
</script>

<template>
  <section>
    <h1>Journaux binaires</h1>
    <p class="muted">Retrouver qui a ajouté, modifié ou supprimé quoi, et quand, dans les journaux archivés. La recherche est faite par
      l'agent du serveur d'archivage ; la production n'est pas sollicitée.</p>
    <div class="toolbar">
      <label>Topologie
        <select v-model="topoId">
          <option v-for="t in topoChoices" :key="t.id" :value="t.id">{{ siteOf(t.siteId)?.name }} [{{ siteOf(t.siteId)?.code }}] — {{ t.name }}</option>
        </select>
      </label>
    </div>
    <p v-if="error" class="error" role="alert">{{ error }}</p>

    <div class="grid2">
      <form v-if="canSearch" class="card form" @submit.prevent="search">
        <h2>Rechercher</h2>
        <div class="grid2">
          <label>Du <input v-model="form.from" type="datetime-local" required /></label>
          <label>Au <input v-model="form.to" type="datetime-local" required /></label>
        </div>
        <fieldset class="checks-row">
          <legend>Opérations</legend>
          <label v-for="(l, k) in typeLabel" :key="k" class="check"><input v-model="form.types[k]" type="checkbox" /> {{ l }}</label>
        </fieldset>
        <label>Tables (base.table, séparées par des virgules ; vide = toutes)
          <input v-model="form.tables" placeholder="prestige.vente, prestige.client" autocomplete="off" />
        </label>
        <p v-if="!tablesOk" class="error small">Forme attendue : base.table</p>
        <label>Bases (facultatif) <input v-model="form.databases" placeholder="prestige" autocomplete="off" /></label>
        <label>Valeur recherchée (facultatif : numéro de ticket, nom, code produit…)
          <input v-model="form.text" maxlength="200" autocomplete="off" />
        </label>
        <label>Motif (journal d'audit) <input v-model="form.reason" required maxlength="300" placeholder="Ventes du 30/10 disparues" /></label>
        <div class="toolbar"><button type="submit" class="primary" :disabled="!topoId || !tablesOk || !form.reason.trim()">Rechercher</button></div>
      </form>

      <div class="card">
        <h2>Recherches récentes</h2>
        <table>
          <tbody>
            <tr v-for="o in recent" :key="o.id" :class="o.id === view?.operation.id ? 'selected' : ''">
              <td class="small">{{ date(o.requestedAt) }}</td>
              <td class="small">{{ o.summary.replace(/^Recherche dans les journaux de [^:]+ : /, '') }}</td>
              <td><span :class="['badge', opStatusClass[o.status]]">{{ opStatusLabel[o.status] }}</span></td>
              <td><RouterLink :to="{ query: { op: o.id } }" class="small">voir</RouterLink></td>
            </tr>
            <tr v-if="!recent.length"><td class="muted">Aucune recherche pour cette topologie.</td></tr>
          </tbody>
        </table>
      </div>
    </div>

    <div v-if="view" class="card">
      <h2>Résultat <span :class="['badge', opStatusClass[view.operation.status]]">{{ opStatusLabel[view.operation.status] }}</span></h2>
      <p class="small muted">{{ view.operation.summary }} · demandée par {{ view.operation.requestedBy }} le {{ date(view.operation.requestedAt) }}
        · période du {{ date(params.from) }} au {{ date(params.to) }}</p>
      <p v-if="running" class="muted">Lecture des journaux en cours…</p>
      <p v-if="view.operation.error" class="error">{{ view.operation.error }}</p>
      <p v-if="result?.purged" class="muted">Aperçu effacé de la console (conservation 30 jours). Le résultat complet reste chiffré dans le stockage de la pharmacie.</p>
      <template v-else-if="result">
        <p>
          <strong>{{ result.total }}</strong> transaction(s), {{ result.rows }} ligne(s) ou instruction(s), dans {{ result.files }} journal(aux),
          en {{ duration(result.totalSeconds) }}.
          <span v-if="result.truncated" class="warn"> Seules les {{ result.transactions.length }} premières sont affichées : précisez la recherche.</span>
        </p>
        <p v-if="result.masked" class="warn-box">Votre rôle ne permet pas de voir les valeurs : seules la date, la nature et la table des changements sont affichées.</p>
        <div v-for="tx in result.transactions" :key="tx.gtid" class="tx">
          <div class="tx-head">
            <span>{{ date(tx.at) }}</span>
            <span class="mono small">GTID {{ tx.gtid }}</span>
            <span class="small muted">serveur {{ tx.serverId }} · {{ tx.file }} position {{ tx.pos }}</span>
            <button v-if="canSearch" class="link" @click="restoreBefore(tx)" title="Préparer une restauration isolée à l'état juste avant cette transaction">Restaurer juste avant</button>
          </div>
          <pre v-for="(q, i) in tx.queries ?? []" :key="i" class="details small">{{ q }}</pre>
          <div v-for="(c, i) in tx.changes" :key="i" class="change">
            <span :class="['badge', typeClass[c.type]]">{{ typeLabel[c.type] }}</span>
            <span class="mono">{{ c.table ? `${c.database}.${c.table}` : c.database || '' }}</span>
            <span>{{ c.statement ? '' : `${c.rows} ligne(s)` }}</span>
            <button v-if="c.sample?.length" class="link small" @click="open[tx.gtid + i] = !open[tx.gtid + i]">{{ open[tx.gtid + i] ? 'masquer' : 'voir les valeurs' }}</button>
            <pre v-if="c.statement" class="details small">{{ c.statement }}</pre>
            <div v-if="open[tx.gtid + i]" class="table-wrap">
              <table class="rowvals small">
                <thead><tr><th v-for="h in grid(c).cols" :key="h">{{ h }}</th></tr></thead>
                <tbody>
                  <tr v-for="(line, j) in grid(c).lines" :key="j">
                    <td v-for="(cell, k) in line" :key="k" :class="['mono', cell.changed ? 'changed' : '']">{{ cell.text }}</td>
                  </tr>
                </tbody>
              </table>
              <p v-if="c.rows > (c.sample?.length ?? 0)" class="muted small">{{ c.sample?.length }} premières lignes sur {{ c.rows }} (toutes dans le résultat complet).</p>
            </div>
          </div>
        </div>
        <p v-if="!result.transactions.length" class="muted">Aucune transaction ne correspond.</p>
        <p class="muted small">Colonnes numérotées dans l'ordre de la table (@1 = première colonne) : les journaux ne contiennent pas leurs noms.
          Résultat complet chiffré : <span class="mono">{{ result.result.relativePath }}</span> ({{ bytes(result.result.sizeBytes) }}),
          lisible avec <span class="mono">prm-agent decrypt</span>. Outil : {{ result.tool }}.</p>
      </template>
    </div>
  </section>
</template>

<style scoped>
.tx { border-top: 1px solid var(--border, #e5e7eb); padding: 0.6rem 0; }
.tx-head { display: flex; gap: 1rem; align-items: baseline; flex-wrap: wrap; }
.tx-head button { margin-left: auto; }
.change { display: flex; gap: 0.7rem; align-items: baseline; flex-wrap: wrap; margin: 0.3rem 0 0 1rem; }
.change pre, .change .table-wrap { flex-basis: 100%; }
.rowvals { margin: 0.2rem 0; width: auto; }
.rowvals td { padding: 0.1rem 0.8rem 0.1rem 0; }
td.changed { font-weight: 600; color: #9a3412; }
tr.selected td { background: #eef4ff; }
.checks-row { display: flex; gap: 1rem; border: none; padding: 0; flex-wrap: wrap; }
</style>
