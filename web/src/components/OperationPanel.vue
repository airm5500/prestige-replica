<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref, watch } from 'vue'
import { api, json, type OperationView } from '../api'
import { useSession } from '../stores/session'
import { bytes, date, duration, opStatusClass, opStatusLabel, opTypeLabel } from '../format'

/** Détail d'une opération : journal, résultat, approbation et annulation. */
const props = defineProps<{ id: string }>()
const emit = defineEmits<{ changed: [] }>()
const session = useSession()
const view = ref<OperationView | null>(null)
const error = ref('')
const confirmation = ref('')
let timer: number | undefined

async function load() {
  try { view.value = await api.operation(props.id); error.value = '' } catch (e) { error.value = (e as Error).message }
}
watch(() => props.id, () => { confirmation.value = ''; load() })
onMounted(() => { load(); timer = window.setInterval(() => { if (active.value) load() }, 4000) })
onUnmounted(() => window.clearInterval(timer))

const op = computed(() => view.value?.operation ?? null)
const active = computed(() => !!op.value && ['AWAITING_APPROVAL', 'QUEUED', 'RUNNING'].includes(op.value.status))
const params = computed(() => json<Record<string, any>>(op.value?.params) ?? {})
const result = computed(() => json<Record<string, any>>(op.value?.result))
const canApprove = computed(() => op.value?.status === 'AWAITING_APPROVAL' && session.can('SUPER_ADMIN', 'APPROVER')
  && op.value.requestedBy !== session.me?.username)
const canCancel = computed(() => !!op.value && ['AWAITING_APPROVAL', 'QUEUED'].includes(op.value.status)
  && session.can('SUPER_ADMIN', 'SITE_ADMIN', 'OPERATOR', 'APPROVER'))

async function act(fn: () => Promise<unknown>) {
  error.value = ''
  try { await fn(); await load(); emit('changed') } catch (e) { error.value = (e as Error).message }
}
const approve = () => act(() => api.approve(props.id, confirmation.value))
const cancel = () => act(() => api.cancelOperation(props.id))
const copy = (t: string) => navigator.clipboard?.writeText(t)
</script>

<template>
  <div v-if="op" class="card op-panel">
    <h2>{{ opTypeLabel[op.type] }} <span class="badge" :class="opStatusClass[op.status]" role="status">{{ opStatusLabel[op.status] }}</span></h2>
    <p>{{ op.summary }}</p>
    <p class="muted small">
      Nœud {{ view?.nodeName }} · pharmacie {{ view?.siteCode }} · demandée par {{ op.requestedBy }} le {{ date(op.requestedAt) }}
      <template v-if="op.approvedBy"> · approuvée par {{ op.approvedBy }} le {{ date(op.approvedAt) }}</template>
      <template v-if="op.startedAt"> · démarrée {{ date(op.startedAt) }}</template>
      <template v-if="op.finishedAt"> · terminée {{ date(op.finishedAt) }}</template>
    </p>

    <div v-if="op.type === 'PROVISION_REPLICA'" class="warn-box">
      <strong>Opération destructive pour la cible.</strong> Le dossier de données <code>{{ params.confirmDataDir }}</code> sera remplacé
      (l'ancien est conservé à côté) puis la réplication repartira de <code>{{ params.sourceHost }}:{{ params.sourcePort }}</code>
      <template v-if="params.delaySeconds"> avec un retard volontaire de {{ duration(params.delaySeconds) }}</template>.
    </div>

    <form v-if="canApprove" class="toolbar" @submit.prevent="approve">
      <label>Pour approuver, ressaisissez le code de la pharmacie ({{ view?.siteCode }})
        <input v-model="confirmation" autocomplete="off" required />
      </label>
      <button type="submit" class="primary">Approuver</button>
    </form>
    <p v-else-if="op.status === 'AWAITING_APPROVAL'" class="muted">
      En attente d'une approbation par une autre personne (rôle Approbateur){{ op.requestedBy === session.me?.username ? ' : vous ne pouvez pas approuver votre propre demande' : '' }}.
    </p>
    <p v-if="canCancel"><button class="danger" @click="cancel">Annuler l'opération</button></p>
    <p v-if="error" class="error" role="alert">{{ error }}</p>
    <p v-if="op.error" class="error">{{ op.error }}</p>

    <template v-if="result && op.type === 'PREFLIGHT'">
      <h3>Prérequis {{ result.ready ? '— prêt' : '— corrections nécessaires' }}</h3>
      <div class="table-wrap"><table>
        <thead><tr><th>Contrôle</th><th>État</th><th>Détail</th></tr></thead>
        <tbody><tr v-for="c in result.checks ?? []" :key="c.id">
          <td class="mono small">{{ c.id }}</td>
          <td :class="c.status === 'OK' ? 'ok' : c.status === 'WARN' ? 'warn' : 'ko'">{{ c.status }}</td>
          <td>{{ c.message }}</td>
        </tr></tbody>
      </table></div>
      <template v-if="result.recommendedConfig">
        <h3>Configuration recommandée (section [mysqld] du my.ini)</h3>
        <pre class="details">{{ result.recommendedConfig }}</pre>
        <p><button @click="copy(result.recommendedConfig)">Copier</button>
          <span class="muted small"> Un redémarrage du service MariaDB est nécessaire pour log_bin et server_id.</span></p>
      </template>
    </template>
    <template v-else-if="result && op.type === 'BACKUP_FULL' && result.manifest">
      <h3>Sauvegarde</h3>
      <dl class="kv">
        <dt>Taille chiffrée</dt><dd>{{ bytes(result.manifest.sizeBytes) }}</dd>
        <dt>Position GTID</dt><dd class="mono">{{ result.manifest.gtid || '—' }}</dd>
        <dt>Empreinte SHA-256</dt><dd class="mono small">{{ result.manifest.sha256 }}</dd>
        <dt>Clé</dt><dd class="mono">{{ result.manifest.keyId }}</dd>
      </dl>
    </template>
    <details v-else-if="result">
      <summary>Résultat détaillé</summary>
      <pre class="details">{{ JSON.stringify(result, null, 2) }}</pre>
    </details>

    <h3>Journal</h3>
    <ol class="events">
      <li v-for="e in view?.events ?? []" :key="e.id" :class="e.level.toLowerCase()">
        <span class="muted small">{{ date(e.at) }}</span> {{ e.message }}
      </li>
      <li v-if="active && op.status !== 'AWAITING_APPROVAL'" class="muted">… mise à jour automatique</li>
    </ol>
  </div>
  <p v-else-if="error" class="error">{{ error }}</p>
</template>
