<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { api, type AuditEvent } from '../api'
import { useSession } from '../stores/session'
import { date } from '../format'

const session = useSession()
const items = ref<AuditEvent[]>([])
const actor = ref('')
const action = ref('')
const error = ref('')
const verification = ref<string>('')

async function load(more = false) {
  try {
    const beforeId = more && items.value.length ? items.value[items.value.length - 1]!.id : undefined
    const page = await api.audit({ actor: actor.value, action: action.value, beforeId })
    items.value = more ? [...items.value, ...page] : page
    error.value = ''
  } catch (e) { error.value = (e as Error).message }
}
async function verify() {
  try {
    const v = await api.verifyAudit()
    verification.value = v.valid ? `Chaîne intègre : ${v.checked} entrées vérifiées.` : `ALERTE : chaîne rompue à l'entrée ${v.firstBrokenId}.`
  } catch (e) { verification.value = (e as Error).message }
}
function exportCsv() {
  const q = new URLSearchParams()
  if (actor.value) q.set('actor', actor.value)
  if (action.value) q.set('action', action.value)
  window.location.href = `api/v1/audit/export?${q}`
}
onMounted(() => load())
</script>

<template>
  <section>
    <h1>Journal d'audit</h1>
    <p class="muted">Journal en ajout seul, chaîné par empreinte SHA-256. Toutes les actions administratives y sont attribuées et horodatées (UTC).</p>
    <form class="toolbar" @submit.prevent="load()">
      <label>Utilisateur <input v-model="actor" /></label>
      <label>Action <input v-model="action" placeholder="ex. NODE_REGISTER" /></label>
      <button type="submit">Filtrer</button>
      <button type="button" @click="exportCsv">Exporter CSV</button>
      <button v-if="session.can('SUPER_ADMIN', 'AUDITOR')" type="button" @click="verify">Vérifier l'intégrité</button>
    </form>
    <p v-if="verification" :class="verification.startsWith('ALERTE') ? 'error' : 'ok'" role="status">{{ verification }}</p>
    <p v-if="error" class="error" role="alert">{{ error }}</p>
    <div class="table-wrap"><table>
      <thead><tr><th>#</th><th>Date</th><th>Utilisateur</th><th>Action</th><th>Ressource</th><th>Résultat</th><th>Détails</th></tr></thead>
      <tbody>
        <tr v-for="e in items" :key="e.id">
          <td>{{ e.id }}</td><td>{{ date(e.at) }}</td><td>{{ e.actor }}</td><td class="mono">{{ e.action }}</td>
          <td class="mono small">{{ e.resource ?? '—' }}</td><td>{{ e.result }}</td><td class="small">{{ e.details ?? '' }}</td>
        </tr>
      </tbody>
    </table></div>
    <button v-if="items.length && items.length % 100 === 0" @click="load(true)">Charger plus</button>
  </section>
</template>
