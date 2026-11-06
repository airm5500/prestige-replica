<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { api, type NodeRole, type NotificationChannel, type Severity, type Tenant, type Topology, type UserOut } from '../api'
import { useSession } from '../stores/session'
import { roleLabel } from '../format'

const session = useSession()
const tenants = ref<Tenant[]>([])
const topologies = ref<Topology[]>([])
const channels = ref<NotificationChannel[]>([])
const users = ref<UserOut[]>([])
const message = ref('')
const error = ref('')
const token = ref<{ agentToken: string; warning: string } | null>(null)

const isSuper = computed(() => session.can('SUPER_ADMIN'))
const canEdit = computed(() => session.can('SUPER_ADMIN', 'SITE_ADMIN'))

const tForm = ref({ code: '', name: '' })
const sForm = ref({ tenantId: '', code: '', name: '', environment: 'production', color: '#2563eb', timezone: 'Africa/Abidjan' })
const topoForm = ref({ siteId: '', name: 'prestige-prod', mode: 'A' })
const nForm = ref({ siteId: '', topologyId: '', name: '', expectedRole: 'PRIMARY' as NodeRole, addressLabel: '' })
const cForm = ref({ tenantId: '', siteId: '', type: 'EMAIL' as 'EMAIL' | 'WEBHOOK', target: '', minSeverity: 'WARNING' as Severity })
const uForm = ref({ username: '', password: '', role: 'OPERATOR', tenantId: '', siteId: '', displayName: '' })

const topoForSite = computed(() => topologies.value.filter((t) => t.siteId === nForm.value.siteId))
const siteName = (id: string | null) => (id ? session.sites.find((s) => s.id === id)?.name ?? id : 'tous les sites')

async function load() {
  try {
    tenants.value = await api.tenants()
    topologies.value = await api.topologies()
    channels.value = await api.channels().catch(() => [])
    users.value = isSuper.value ? await api.users() : []
    await session.load()
  } catch (e) { error.value = (e as Error).message }
}
async function run(fn: () => Promise<unknown>, ok: string) {
  error.value = ''; message.value = ''
  try { await fn(); message.value = ok; await load() } catch (e) { error.value = (e as Error).message }
}
const createTenant = () => run(() => api.createTenant(tForm.value), 'Organisation créée')
const createSite = () => run(() => api.createSite(sForm.value), 'Pharmacie créée')
const createTopology = () => run(() => api.createTopology(topoForm.value), 'Topologie créée')
const registerNode = () => run(async () => {
  token.value = await api.registerNode({ ...nForm.value, topologyId: nForm.value.topologyId || null })
}, 'Nœud enregistré')
const toggleMaintenance = (t: Topology) => run(() => api.updateTopology(t.id, { ...t, maintenance: !t.maintenance }),
  t.maintenance ? 'Fin de maintenance' : 'Topologie en maintenance : alertes suspendues')
const createChannel = () => run(() => api.createChannel({ ...cForm.value, siteId: cForm.value.siteId || null }), 'Canal de notification ajouté')
const disableChannel = (c: NotificationChannel) => run(() => api.disableChannel(c.id), 'Canal désactivé')
const createUser = () => run(() => api.createUser({ ...uForm.value, tenantId: uForm.value.tenantId || null, siteId: uForm.value.siteId || null }), 'Utilisateur créé')

onMounted(load)
</script>

<template>
  <section>
    <h1>Paramètres et inventaire</h1>
    <p v-if="message" class="ok" role="status">{{ message }}</p>
    <p v-if="error" class="error" role="alert">{{ error }}</p>

    <div v-if="token" class="card warn" role="alert">
      <h2>Jeton de l'agent — affiché une seule fois</h2>
      <p>{{ token.warning }}</p>
      <pre class="mono token">{{ token.agentToken }}</pre>
      <button @click="token = null">J'ai copié le jeton</button>
    </div>

    <div class="grid2">
      <form v-if="isSuper && session.me?.global" class="card" @submit.prevent="createTenant">
        <h2>Organisation (tenant)</h2>
        <label>Code <input v-model="tForm.code" required pattern="[A-Za-z0-9_-]{2,32}" /></label>
        <label>Nom <input v-model="tForm.name" required /></label>
        <button class="primary">Créer</button>
      </form>

      <form v-if="isSuper" class="card" @submit.prevent="createSite">
        <h2>Pharmacie (site)</h2>
        <label>Organisation <select v-model="sForm.tenantId" required><option v-for="t in tenants" :key="t.id" :value="t.id">{{ t.name }}</option></select></label>
        <label>Code court (ressaisi pour les actions critiques) <input v-model="sForm.code" required pattern="[A-Za-z0-9_-]{2,16}" /></label>
        <label>Nom <input v-model="sForm.name" required /></label>
        <label>Environnement <select v-model="sForm.environment"><option>production</option><option>recette</option><option>laboratoire</option></select></label>
        <label>Couleur <input v-model="sForm.color" type="color" /></label>
        <label>Fuseau horaire <input v-model="sForm.timezone" /></label>
        <button class="primary">Créer</button>
      </form>

      <form v-if="canEdit" class="card" @submit.prevent="createTopology">
        <h2>Topologie</h2>
        <label>Pharmacie <select v-model="topoForm.siteId" required><option v-for="s in session.sites" :key="s.id" :value="s.id">{{ s.name }}</option></select></label>
        <label>Nom <input v-model="topoForm.name" required /></label>
        <label>Mode
          <select v-model="topoForm.mode">
            <option value="A">A — continuité standard</option><option value="B">B — protection renforcée</option>
            <option value="C">C — reprise après sinistre</option><option value="D">D — base relais protégée</option>
          </select>
        </label>
        <button class="primary">Créer</button>
      </form>

      <form v-if="canEdit" class="card" @submit.prevent="registerNode">
        <h2>Serveur MariaDB (nœud)</h2>
        <label>Pharmacie <select v-model="nForm.siteId" required><option v-for="s in session.sites" :key="s.id" :value="s.id">{{ s.name }}</option></select></label>
        <label>Topologie <select v-model="nForm.topologyId"><option value="">(aucune)</option><option v-for="t in topoForSite" :key="t.id" :value="t.id">{{ t.name }}</option></select></label>
        <label>Nom <input v-model="nForm.name" required placeholder="ex. srv-prestige-b" /></label>
        <label>Rôle attendu <select v-model="nForm.expectedRole"><option v-for="(l, r) in roleLabel" :key="r" :value="r">{{ l }}</option></select></label>
        <label>Adresse (information) <input v-model="nForm.addressLabel" placeholder="ex. 192.168.1.20:3306" /></label>
        <button class="primary">Enregistrer et obtenir le jeton</button>
      </form>
    </div>

    <div class="card">
      <h2>Seuils et maintenance des topologies</h2>
      <div class="table-wrap"><table>
        <thead><tr><th>Topologie</th><th>Pharmacie</th><th>Mode</th><th>Retard avert. / crit.</th><th>Disque avert. / crit.</th><th>Silence agent</th><th>Maintenance</th></tr></thead>
        <tbody>
          <tr v-for="t in topologies" :key="t.id">
            <td>{{ t.name }}</td><td>{{ siteName(t.siteId) }}</td><td>{{ t.mode }}</td>
            <td>{{ t.lagWarnSeconds }} s / {{ t.lagCritSeconds }} s</td><td>{{ t.diskWarnPct }} % / {{ t.diskCritPct }} %</td>
            <td>{{ t.agentSilenceSeconds }} s</td>
            <td><button v-if="canEdit" @click="toggleMaintenance(t)">{{ t.maintenance ? 'Terminer' : 'Démarrer' }}</button><span v-else>{{ t.maintenance ? 'oui' : 'non' }}</span></td>
          </tr>
        </tbody>
      </table></div>
    </div>

    <div class="card">
      <h2>Notifications</h2>
      <p class="muted small">SMS et WhatsApp : utilisez un webhook vers votre passerelle (connecteur remplaçable).</p>
      <ul>
        <li v-for="c in channels" :key="c.id">{{ c.type }} → {{ c.target }} · {{ siteName(c.siteId) }} · à partir de {{ c.minSeverity }}
          <span v-if="!c.enabled" class="muted">(désactivé)</span>
          <button v-else-if="canEdit" class="link" @click="disableChannel(c)">désactiver</button></li>
      </ul>
      <form v-if="canEdit" class="inline" @submit.prevent="createChannel">
        <select v-model="cForm.tenantId" required aria-label="Organisation"><option v-for="t in tenants" :key="t.id" :value="t.id">{{ t.name }}</option></select>
        <select v-model="cForm.siteId" aria-label="Pharmacie"><option value="">Toutes les pharmacies</option><option v-for="s in session.sites.filter((x) => x.tenantId === cForm.tenantId)" :key="s.id" :value="s.id">{{ s.name }}</option></select>
        <select v-model="cForm.type" aria-label="Type"><option>EMAIL</option><option>WEBHOOK</option></select>
        <input v-model="cForm.target" required :placeholder="cForm.type === 'EMAIL' ? 'astreinte@exemple.ci' : 'https://passerelle/sms'" aria-label="Destination" />
        <select v-model="cForm.minSeverity" aria-label="Niveau minimal"><option>INFO</option><option>WARNING</option><option>CRITICAL</option></select>
        <button>Ajouter</button>
      </form>
    </div>

    <div v-if="isSuper" class="card">
      <h2>Utilisateurs</h2>
      <ul><li v-for="u in users" :key="u.id">{{ u.username }} · {{ u.role }} · {{ u.tenantId ? (tenants.find((t) => t.id === u.tenantId)?.name ?? u.tenantId) : 'global' }} · {{ siteName(u.siteId) }}</li></ul>
      <form class="inline" @submit.prevent="createUser">
        <input v-model="uForm.username" required placeholder="identifiant" aria-label="Identifiant" />
        <input v-model="uForm.password" required type="password" minlength="12" placeholder="mot de passe (12+)" aria-label="Mot de passe" autocomplete="new-password" />
        <select v-model="uForm.role" aria-label="Rôle">
          <option value="SUPER_ADMIN">Super-administrateur</option><option value="OPERATOR">Exploitant</option>
          <option value="APPROVER">Approbateur</option><option value="SITE_ADMIN">Administrateur de pharmacie</option>
          <option value="AUDITOR">Auditeur</option><option value="READ_ONLY">Lecture seule</option>
        </select>
        <select v-model="uForm.tenantId" aria-label="Organisation"><option value="">(global)</option><option v-for="t in tenants" :key="t.id" :value="t.id">{{ t.name }}</option></select>
        <select v-model="uForm.siteId" aria-label="Pharmacie"><option value="">(toutes)</option><option v-for="s in session.sites.filter((x) => x.tenantId === uForm.tenantId)" :key="s.id" :value="s.id">{{ s.name }}</option></select>
        <input v-model="uForm.displayName" placeholder="nom affiché" aria-label="Nom affiché" />
        <button>Créer</button>
      </form>
    </div>
  </section>
</template>
