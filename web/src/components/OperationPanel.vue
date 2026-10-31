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
const seconds = (ms: number | undefined) => (ms === undefined || ms === null ? '—' : `${(ms / 1000).toFixed(1).replace('.', ',')} s`)
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

    <div v-if="op.type === 'SWITCHOVER'" class="warn-box">
      <strong>Bascule du primaire de {{ view?.topologyName }}.</strong> Les écritures de Prestige seront interrompues quelques secondes :
      l'ancien primaire passe en lecture seule et ses connexions sont coupées, puis <code>{{ params.newAddress }}</code> devient la source
      une fois toutes les transactions appliquées. Si une étape échoue, l'ancien primaire est rouvert en écriture.
      Après la bascule, Prestige doit être pointé vers <code>{{ params.newAddress }}</code>.
    </div>

    <div v-if="op.type === 'DRILL'" class="warn-box">
      <strong>Exercice de bascule de {{ view?.topologyName }}.</strong> Aller vers <code>{{ params.newAddress }}</code> puis retour sur
      <code>{{ params.oldAddress }}</code>, sans perte ; les écritures de Prestige sont interrompues quelques secondes à chaque sens.
      <template v-if="params.notBefore"> Démarrage programmé à {{ date(params.notBefore) }}.</template>
    </div>

    <div v-if="op.type === 'FAILOVER'" class="fence-box">
      <strong>Bascule d'urgence de {{ view?.topologyName }} : le primaire est perdu.</strong>
      L'ancien primaire sera isolé (lecture seule ou arrêt du service), puis <code>{{ params.newAddress }}</code> deviendra la source
      après avoir appliqué tout ce qu'elle a reçu. <strong>Perte estimée :</strong> {{ params.lossEstimate }}
      <template v-if="params.fencingConfirmed"> L'isolement manuel de l'ancien primaire a été confirmé par le demandeur.</template>
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

    <template v-if="op.type === 'DRILL' && result">
      <h3>Bilan de l'exercice</h3>
      <dl class="kv">
        <dt>Interruption à l'aller</dt><dd>{{ seconds(result.outbound?.writeUnavailableMs) }}</dd>
        <dt>Interruption au retour</dt><dd>{{ result.phase === 2 ? seconds(result.writeUnavailableMs) : '—' }}</dd>
        <dt>Durée totale</dt><dd>{{ duration(result.totalSeconds) }}</dd>
        <dt>Primaire à la fin</dt><dd class="mono">{{ result.newPrimary }}</dd>
        <dt>Ventes confirmées perdues</dt><dd>{{ op.status === 'SUCCEEDED' ? 'aucune (chaque promotion a attendu la dernière transaction)' : '—' }}</dd>
      </dl>
    </template>
    <template v-if="(op.type === 'SWITCHOVER' || op.type === 'FAILOVER') && result">
      <h3>Bilan</h3>
      <dl class="kv">
        <dt>Primaire actuel</dt><dd class="mono">{{ result.newPrimary }}</dd>
        <dt>Écritures interrompues</dt><dd>{{ seconds(result.writeUnavailableMs) }}</dd>
        <dt>Durée totale</dt><dd>{{ duration(result.totalSeconds) }}</dd>
        <template v-if="op.type === 'FAILOVER'"><dt>Perte estimée</dt><dd>{{ result.lossEstimate ?? '—' }}</dd></template>
        <template v-else><dt>Dernière transaction de l'ancien primaire</dt><dd class="mono">{{ result.finalGtid ?? '—' }}</dd></template>
        <dt>Position de la nouvelle source</dt><dd class="mono">{{ result.newPrimaryGtid ?? '—' }}</dd>
      </dl>
    </template>

    <template v-if="view?.steps?.length">
      <h3>Étapes</h3>
      <ol class="steps">
        <li v-for="st in view.steps" :key="st.operation.id">
          <span class="badge" :class="opStatusClass[st.operation.status]">{{ opStatusLabel[st.operation.status] }}</span>
          <strong>{{ opTypeLabel[st.operation.type] }}</strong> — {{ st.nodeName }}
          <div class="muted small">{{ st.events.at(-1)?.message }}</div>
          <div v-if="st.operation.error" class="error small">{{ st.operation.error }}</div>
        </li>
      </ol>
    </template>

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
    <template v-else-if="op.type === 'BINLOG_SEARCH'">
      <p v-if="result && !result.purged">{{ result.total }} transaction(s) trouvée(s), {{ result.rows }} ligne(s) ou instruction(s).</p>
      <p><RouterLink :to="{ path: '/journaux', query: { op: op.id } }">Voir le résultat dans l'explorateur des journaux →</RouterLink></p>
    </template>
    <template v-else-if="result && op.type === 'PITR_RESTORE'">
      <h3>Restauration à un instant précis</h3>
      <dl class="kv">
        <dt>Instant visé</dt><dd>{{ result.stopBefore ? `juste avant la transaction ${result.stopBefore.gtid}` : date(result.stopAt) }}</dd>
        <dt>Point de départ (sauvegarde)</dt><dd class="mono">{{ result.startGtid }}</dd>
        <dt>Dernière transaction rejouée</dt><dd class="mono">{{ result.reachedGtid || '—' }}<span v-if="result.lastTransactionAt"> ({{ date(result.lastTransactionAt) }})</span></dd>
        <dt>Transactions rejouées</dt><dd>{{ result.transactions }} depuis {{ result.binlogFiles }} journal(aux)</dd>
        <dt>Durée</dt><dd>{{ duration(result.totalSeconds) }}</dd>
      </dl>
      <div v-if="(result.gaps ?? []).length" class="warn-box">Transactions absentes de l'archive : {{ result.gaps.join(' ; ') }}</div>
      <template v-if="(result.extracted ?? []).length">
        <h3>Tables extraites (chiffrées, dans le stockage des sauvegardes)</h3>
        <table><thead><tr><th>Table</th><th>Fichier</th><th>Taille</th></tr></thead><tbody>
          <tr v-for="x in result.extracted" :key="x.table"><td class="mono">{{ x.table }}</td><td class="mono small">{{ x.relativePath }}</td><td>{{ bytes(x.sizeBytes) }}</td></tr>
        </tbody></table>
        <p class="small">Pour lire un fichier, sur un serveur qui possède la clé de sauvegarde :</p>
        <pre class="details">prm-agent decrypt -config agent.json -in "&lt;stockage&gt;\{{ result.extracted[0].relativePath.replaceAll('/', '\\') }}" -out {{ result.extracted[0].table }}.sql</pre>
        <p class="muted small">Le fichier obtenu est un export SQL (CREATE TABLE + INSERT). Importez-le dans une base de travail, jamais directement
          dans la production, puis recopiez les seules lignes à récupérer.</p>
      </template>
    </template>
    <details v-else-if="result && op.type !== 'SWITCHOVER' && op.type !== 'FAILOVER' && op.type !== 'DRILL'">
      <summary>Résultat détaillé</summary>
      <pre class="details">{{ JSON.stringify(result, null, 2) }}</pre>
    </details>

    <p><RouterLink :to="`/operations/${op.id}/report`">Rapport d'opération imprimable →</RouterLink></p>
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
