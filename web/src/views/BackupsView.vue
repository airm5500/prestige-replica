<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref, watch } from 'vue'
import { useRouter } from 'vue-router'
import { api, type Backup, type BackupPolicy, type NodeSummary, type SiteSummary, type Topology } from '../api'
import { useSession } from '../stores/session'
import { bytes, date, duration, roleLabel, weekdayLabel } from '../format'

const session = useSession()
const router = useRouter()
const fleet = ref<SiteSummary[]>([])
const topologies = ref<Topology[]>([])
const backups = ref<Backup[]>([])
const topoId = ref(localStorage.getItem('prm.backupTopo') ?? '')
const error = ref('')
const message = ref('')
const policy = ref<BackupPolicy | null>(null)
const wizard = ref<{ targetNodeId: string; backupId: string; confirmDataDir: string; delayHours: number; topologyConfirmation: string } | null>(null)
let timer: number | undefined

const canOperate = computed(() => session.can('SUPER_ADMIN', 'SITE_ADMIN', 'OPERATOR'))
const canPolicy = computed(() => session.can('SUPER_ADMIN', 'SITE_ADMIN'))
const topoChoices = computed(() => topologies.value.filter((t) => !session.activeSiteId || t.siteId === session.activeSiteId))
const topo = computed(() => topologies.value.find((t) => t.id === topoId.value) ?? null)
const nodes = computed<NodeSummary[]>(() => fleet.value.flatMap((s) => s.topologies).find((t) => t.id === topoId.value)?.nodes ?? [])
const nodeName = (id: string | null) => nodes.value.find((n) => n.id === id)?.name ?? '—'
const siteOf = (siteId: string) => session.sites.find((s) => s.id === siteId)
const replicas = computed(() => nodes.value.filter((n) => n.expectedRole === 'REPLICA' || n.expectedRole === 'DELAYED_REPLICA'))
const verified = computed(() => backups.value.filter((b) => b.status === 'COMPLETED' && b.verifyStatus === 'VERIFIED' && b.gtid))
const latest = computed(() => backups.value.find((b) => b.status === 'COMPLETED') ?? null)
const wizardTarget = computed(() => replicas.value.find((n) => n.id === wizard.value?.targetNodeId) ?? null)

async function load() {
  try {
    ;[fleet.value, topologies.value] = await Promise.all([api.fleet(), api.topologies()])
    if (!topoChoices.value.some((t) => t.id === topoId.value)) topoId.value = topoChoices.value[0]?.id ?? ''
    backups.value = topoId.value ? await api.backups(topoId.value) : []
    error.value = ''
  } catch (e) { error.value = (e as Error).message }
}
watch(topoId, (v) => { localStorage.setItem('prm.backupTopo', v); policy.value = null; wizard.value = null; load() })
watch(() => session.activeSiteId, load)
onMounted(() => { load(); timer = window.setInterval(load, 15_000) })
onUnmounted(() => window.clearInterval(timer))

async function run(fn: () => Promise<{ id: string } | unknown>, ok: string, open = true) {
  error.value = ''; message.value = ''
  try {
    const r = await fn()
    message.value = ok
    await load()
    if (open && r && typeof r === 'object' && 'id' in r && 'type' in r) router.push({ path: '/operations', query: { op: (r as { id: string }).id } })
  } catch (e) { error.value = (e as Error).message }
}
const backupNow = () => { const n = topo.value?.backupNodeId ?? replicas.value[0]?.id ?? nodes.value[0]?.id; if (n) run(() => api.backupNow(n), 'Sauvegarde demandée') }
const verify = (b: Backup) => run(() => api.verifyBackup(b.id, topo.value?.restoreNodeId ?? null), 'Restauration de contrôle demandée')

function editPolicy() {
  const t = topo.value!
  policy.value = { backupEnabled: t.backupEnabled, backupNodeId: t.backupNodeId ?? replicas.value[0]?.id ?? null,
    restoreNodeId: t.restoreNodeId ?? t.backupNodeId ?? replicas.value[0]?.id ?? null, backupHour: t.backupHour, backupKeep: t.backupKeep,
    restoreTestWeekday: t.restoreTestWeekday, backupMaxAgeHours: t.backupMaxAgeHours }
}
const savePolicy = () => run(async () => { await api.setBackupPolicy(topoId.value, policy.value!); policy.value = null }, 'Politique enregistrée', false)

function openWizard() {
  const target = replicas.value[0]
  wizard.value = { targetNodeId: target?.id ?? '', backupId: verified.value[0]?.id ?? '', confirmDataDir: '', delayHours: 0, topologyConfirmation: '' }
}
const submitWizard = () => {
  const w = wizard.value!
  run(() => api.provision(topoId.value, { targetNodeId: w.targetNodeId, backupId: w.backupId, confirmDataDir: w.confirmDataDir.trim(),
    delaySeconds: Math.round(w.delayHours * 3600), topologyConfirmation: w.topologyConfirmation }), 'Demande envoyée : une seconde personne doit approuver')
}
const verifyLabel = { NOT_VERIFIED: 'Non vérifiée', VERIFIED: 'Vérifiée', FAILED: 'Échec' } as const
const statusLabel = { RUNNING: 'En cours', COMPLETED: 'Terminée', FAILED: 'Échec', DELETED: 'Purgée' } as const
</script>

<template>
  <section>
    <h1>Sauvegardes et répliques</h1>
    <div class="toolbar">
      <label>Topologie
        <select v-model="topoId">
          <option v-for="t in topoChoices" :key="t.id" :value="t.id">{{ siteOf(t.siteId)?.name }} [{{ siteOf(t.siteId)?.code }}] — {{ t.name }}</option>
        </select>
      </label>
      <template v-if="canOperate && topo">
        <button class="primary" :disabled="!nodes.length" @click="backupNow">Sauvegarder maintenant</button>
        <button :disabled="!verified.length || !replicas.length" @click="openWizard"
                :title="verified.length ? '' : 'Une sauvegarde vérifiée par restauration est nécessaire'">Construire une réplique…</button>
      </template>
    </div>
    <p v-if="message" class="ok" role="status">{{ message }}</p>
    <p v-if="error" class="error" role="alert">{{ error }}</p>
    <p v-if="!topoChoices.length" class="muted">Aucune topologie : créez-en une dans Paramètres.</p>

    <div v-if="topo" class="grid2">
      <div class="card">
        <h2>Politique de sauvegarde</h2>
        <dl v-if="!policy" class="kv">
          <dt>Sauvegarde automatique</dt><dd>{{ topo.backupEnabled ? `tous les jours à ${topo.backupHour} h (heure de la pharmacie)` : 'désactivée' }}</dd>
          <dt>Nœud sauvegardé</dt><dd>{{ nodeName(topo.backupNodeId) }}</dd>
          <dt>Restauration de contrôle</dt><dd>chaque {{ weekdayLabel[topo.restoreTestWeekday] }} à {{ (topo.backupHour + 2) % 24 }} h sur {{ nodeName(topo.restoreNodeId ?? topo.backupNodeId) }}</dd>
          <dt>Conservation</dt><dd>{{ topo.backupKeep }} sauvegardes (la dernière vérifiée est toujours gardée)</dd>
          <dt>Alerte si plus ancienne que</dt><dd>{{ topo.backupMaxAgeHours }} h</dd>
          <dt>Dernière sauvegarde</dt><dd>{{ latest ? date(latest.startedAt) : 'aucune' }}</dd>
        </dl>
        <button v-if="!policy && canPolicy" @click="editPolicy">Modifier</button>
        <form v-if="policy" class="form" @submit.prevent="savePolicy">
          <label><input v-model="policy.backupEnabled" type="checkbox" /> Sauvegarde automatique quotidienne</label>
          <label>Nœud à sauvegarder (une réplique de préférence, pour ne pas charger le primaire)
            <select v-model="policy.backupNodeId"><option v-for="n in nodes" :key="n.id" :value="n.id">{{ n.name }} ({{ roleLabel[n.expectedRole] }})</option></select>
          </label>
          <label>Nœud qui réalise la restauration de contrôle (doit avoir accès au stockage des sauvegardes)
            <select v-model="policy.restoreNodeId"><option v-for="n in nodes" :key="n.id" :value="n.id">{{ n.name }}</option></select>
          </label>
          <label>Heure <input v-model.number="policy.backupHour" type="number" min="0" max="23" /></label>
          <label>Jour du test de restauration
            <select v-model.number="policy.restoreTestWeekday"><option v-for="d in 7" :key="d" :value="d">{{ weekdayLabel[d] }}</option></select>
          </label>
          <label>Nombre de sauvegardes conservées <input v-model.number="policy.backupKeep" type="number" min="1" max="365" /></label>
          <label>Alerte au-delà de (heures) <input v-model.number="policy.backupMaxAgeHours" type="number" min="2" max="240" /></label>
          <div class="toolbar"><button type="submit" class="primary">Enregistrer</button><button type="button" @click="policy = null">Annuler</button></div>
        </form>
      </div>

      <div v-if="wizard" class="card">
        <h2>Construire une réplique</h2>
        <form class="form" @submit.prevent="submitWizard">
          <label>Réplique à (re)construire
            <select v-model="wizard.targetNodeId" required>
              <option v-for="n in replicas" :key="n.id" :value="n.id">{{ n.name }} — {{ roleLabel[n.expectedRole] }} — {{ n.addressLabel ?? '' }}</option>
            </select>
          </label>
          <label>Sauvegarde vérifiée à utiliser
            <select v-model="wizard.backupId" required>
              <option v-for="b in verified" :key="b.id" :value="b.id">{{ date(b.startedAt) }} — {{ bytes(b.sizeBytes) }} — GTID {{ b.gtid }}</option>
            </select>
          </label>
          <label>Dossier de données MariaDB de la cible (doit correspondre à la configuration de son agent)
            <input v-model="wizard.confirmDataDir" required :placeholder="'C:\\Program Files\\MariaDB 10.6\\data'" />
          </label>
          <p v-if="wizardTarget?.serverId" class="muted small">Observé sur la cible : server_id {{ wizardTarget.serverId }}, version {{ wizardTarget.version }}</p>
          <label>Retard volontaire (heures, 0 = réplique normale)
            <input v-model.number="wizard.delayHours" type="number" min="0" max="168" step="0.5" />
          </label>
          <div class="warn-box">
            Les données actuelles de <strong>{{ wizardTarget?.name }}</strong> seront remplacées (l'ancien dossier est conservé à côté).
            Le service MariaDB de cette machine sera arrêté quelques minutes. Une seconde personne devra approuver.
          </div>
          <label>Pour confirmer, saisissez le nom de la topologie « {{ topo.name }} »
            <input v-model="wizard.topologyConfirmation" required autocomplete="off" />
          </label>
          <div class="toolbar">
            <button type="submit" class="danger" :disabled="wizard.topologyConfirmation !== topo.name">Demander l'approbation</button>
            <button type="button" @click="wizard = null">Annuler</button>
          </div>
        </form>
      </div>
    </div>

    <div v-if="topo" class="card">
      <h2>Catalogue</h2>
      <div class="table-wrap"><table>
        <thead><tr><th>Date</th><th>Nœud</th><th>État</th><th>Taille</th><th>GTID</th><th>Restauration</th><th>Durée de restauration</th><th></th></tr></thead>
        <tbody>
          <tr v-for="b in backups" :key="b.id">
            <td>{{ date(b.startedAt) }}</td>
            <td>{{ nodeName(b.nodeId) }}</td>
            <td :class="b.status === 'FAILED' ? 'ko' : ''">{{ statusLabel[b.status] }}</td>
            <td>{{ bytes(b.sizeBytes) }}</td>
            <td class="mono small">{{ b.gtid ?? '—' }}</td>
            <td :class="b.verifyStatus === 'VERIFIED' ? 'ok' : b.verifyStatus === 'FAILED' ? 'ko' : 'muted'">
              {{ verifyLabel[b.verifyStatus] }}<span v-if="b.verifiedAt" class="small"> le {{ date(b.verifiedAt) }}</span></td>
            <td>{{ duration(b.restoreSeconds) }}</td>
            <td>
              <button v-if="canOperate && b.status === 'COMPLETED'" class="link" @click="verify(b)">Tester la restauration</button>
              <RouterLink v-if="b.operationId" :to="{ path: '/operations', query: { op: b.operationId } }" class="small">journal</RouterLink>
            </td>
          </tr>
          <tr v-if="!backups.length"><td colspan="8" class="muted">Aucune sauvegarde pour cette topologie.</td></tr>
        </tbody>
      </table></div>
      <p class="muted small">Une sauvegarde n'est considérée fiable qu'après une restauration de contrôle réussie : seules celles-ci
        peuvent servir à construire une réplique. La durée de restauration mesurée sert d'estimation du temps de reprise.</p>
    </div>
  </section>
</template>
