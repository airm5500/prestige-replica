<script setup lang="ts">
import { onMounted, onUnmounted, ref } from 'vue'
import { useSession } from './stores/session'
import { api } from './api'

const session = useSession()
const ready = ref(false)
const username = ref('')
const password = ref('')
const error = ref('')

async function boot() {
  try { await session.load() } catch { session.me = null }
  ready.value = true
}
async function login() {
  error.value = ''
  try {
    await api.login(username.value, password.value)
    password.value = ''
    await session.load()
  } catch (e) {
    error.value = (e as Error).message
  }
}
const onUnauthorized = () => { session.me = null }
onMounted(() => { window.addEventListener('prm:unauthorized', onUnauthorized); boot() })
onUnmounted(() => window.removeEventListener('prm:unauthorized', onUnauthorized))
</script>

<template>
  <div v-if="!ready" class="center">Chargement…</div>

  <main v-else-if="!session.me" class="login">
    <form @submit.prevent="login" aria-labelledby="login-title">
      <h1 id="login-title">Prestige Replica Manager</h1>
      <p class="muted">Supervision de la réplication MariaDB</p>
      <label>Identifiant <input v-model="username" autocomplete="username" required /></label>
      <label>Mot de passe <input v-model="password" type="password" autocomplete="current-password" required /></label>
      <p v-if="error" class="error" role="alert">{{ error }}</p>
      <button type="submit" class="primary">Se connecter</button>
    </form>
  </main>

  <template v-else>
    <header class="topbar">
      <strong class="brand">Prestige Replica Manager</strong>
      <nav aria-label="Navigation principale">
        <RouterLink to="/">Parc</RouterLink>
        <RouterLink to="/incidents">Incidents</RouterLink>
        <RouterLink to="/backups">Sauvegardes</RouterLink>
        <RouterLink to="/journaux">Journaux</RouterLink>
        <RouterLink to="/coherence">Cohérence</RouterLink>
        <RouterLink to="/switchover">Bascule</RouterLink>
        <RouterLink to="/operations">Opérations</RouterLink>
        <RouterLink v-if="session.me.role !== 'READ_ONLY'" to="/audit">Audit</RouterLink>
        <RouterLink v-if="session.can('SUPER_ADMIN', 'SITE_ADMIN', 'OPERATOR')" to="/settings">Paramètres</RouterLink>
      </nav>
      <label class="site-picker">
        <span class="sr-only">Pharmacie active</span>
        <select :value="session.activeSiteId" @change="session.setActive(($event.target as HTMLSelectElement).value)">
          <option value="">Toutes les pharmacies</option>
          <option v-for="s in session.sites" :key="s.id" :value="s.id">{{ s.name }} [{{ s.code }}]</option>
        </select>
      </label>
      <span class="user">{{ session.me.displayName ?? session.me.username }} · {{ session.me.role }}</span>
      <button class="link" @click="session.logout()">Déconnexion</button>
    </header>
    <div v-if="session.activeSite" class="site-banner" :style="{ borderColor: session.activeSite.color }">
      <span class="dot" :style="{ background: session.activeSite.color }"></span>
      Pharmacie active : <strong>{{ session.activeSite.name }}</strong> — code {{ session.activeSite.code }} — environnement {{ session.activeSite.environment }}
    </div>
    <main class="content"><RouterView /></main>
    <footer class="foot muted">Toute action sur MariaDB passe par le centre d'opérations, est exécutée par l'agent local et tracée dans l'audit.</footer>
  </template>
</template>
