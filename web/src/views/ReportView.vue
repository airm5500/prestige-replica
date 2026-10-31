<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { api, json, type OperationView } from '../api'
import { date, duration, opStatusLabel, opTypeLabel } from '../format'

/** Rapport d'opération imprimable (§11 : planifier → approuver → exécuter → vérifier → auditer). */
const props = defineProps<{ id: string }>()
const view = ref<OperationView | null>(null)
const error = ref('')
onMounted(async () => {
  try { view.value = await api.operation(props.id) } catch (e) { error.value = (e as Error).message }
})
const op = computed(() => view.value?.operation ?? null)
const result = computed(() => json<Record<string, any>>(op.value?.result))
const generated = new Date().toLocaleString('fr-FR')
const all = computed(() => (view.value ? [view.value, ...view.value.steps] : []))
</script>

<template>
  <article v-if="op && view" class="report">
    <p class="no-print"><button class="primary" onclick="window.print()">Imprimer / enregistrer en PDF</button>
      <RouterLink :to="{ path: '/operations', query: { op: op.id } }"> ← retour</RouterLink></p>
    <header>
      <h1>Rapport d'opération — {{ opTypeLabel[op.type] }}</h1>
      <p>{{ view.siteName }} [{{ view.siteCode }}] · topologie {{ view.topologyName ?? '—' }} · référence <span class="mono">{{ op.id }}</span></p>
    </header>
    <table class="report-table">
      <tbody>
        <tr><th>Objet</th><td>{{ op.summary }}</td></tr>
        <tr><th>Résultat</th><td><strong>{{ opStatusLabel[op.status] }}</strong><span v-if="op.error"> — {{ op.error }}</span></td></tr>
        <tr><th>Demandée</th><td>{{ op.requestedBy }}, {{ date(op.requestedAt) }}</td></tr>
        <tr v-if="op.requiresApproval"><th>Approuvée</th><td>{{ op.approvedBy ? `${op.approvedBy}, ${date(op.approvedAt)}` : 'non approuvée' }}</td></tr>
        <tr><th>Exécution</th><td>{{ date(op.startedAt) }} → {{ date(op.finishedAt) }}</td></tr>
        <template v-if="(op.type === 'SWITCHOVER' || op.type === 'FAILOVER') && result">
          <tr><th>Primaire après l'opération</th><td class="mono">{{ result.newPrimary }}</td></tr>
          <tr><th>{{ op.type === 'FAILOVER' ? 'Durée sans primaire (depuis le lancement)' : 'Écritures interrompues' }}</th><td>{{ result.writeUnavailableMs != null ? `${(result.writeUnavailableMs / 1000).toFixed(1).replace('.', ',')} s` : '—' }}</td></tr>
          <tr><th>Durée totale</th><td>{{ duration(result.totalSeconds) }}</td></tr>
          <tr v-if="op.type === 'SWITCHOVER'"><th>Dernière transaction de l'ancien primaire</th><td class="mono">{{ result.finalGtid ?? '—' }}</td></tr>
          <tr v-if="op.type === 'FAILOVER'"><th>Perte estimée</th><td>{{ result.lossEstimate ?? '—' }}</td></tr>
          <tr v-else><th>Transactions perdues</th><td>{{ result.newPrimaryGtid ? 'aucune : la nouvelle source a appliqué cette position avant d\'être ouverte en écriture' : '—' }}</td></tr>
        </template>
      </tbody>
    </table>
    <section v-for="v in all" :key="v.operation.id">
      <h2>{{ v === view ? 'Journal de l\'opération' : `${opTypeLabel[v.operation.type]} — ${v.nodeName}` }}
        <small v-if="v !== view">({{ opStatusLabel[v.operation.status] }})</small></h2>
      <table class="report-table">
        <tbody>
          <tr v-for="e in v.events" :key="e.id" :class="e.level.toLowerCase()">
            <td class="nowrap small">{{ date(e.at) }}</td><td>{{ e.message }}</td>
          </tr>
        </tbody>
      </table>
    </section>
    <footer class="muted small">Généré le {{ generated }} par Prestige Replica Manager. Toutes les actions ci-dessus figurent dans le journal d'audit chaîné.</footer>
  </article>
  <p v-else-if="error" class="error">{{ error }}</p>
</template>
