import { createApp } from 'vue'
import { createPinia } from 'pinia'
import { createRouter, createWebHashHistory } from 'vue-router'
import App from './App.vue'
import './style.css'

const router = createRouter({
  history: createWebHashHistory(),
  routes: [
    { path: '/', component: () => import('./views/FleetView.vue') },
    { path: '/nodes/:id', component: () => import('./views/NodeView.vue'), props: true },
    { path: '/incidents', component: () => import('./views/IncidentsView.vue') },
    { path: '/operations', component: () => import('./views/OperationsView.vue') },
    { path: '/operations/:id/report', component: () => import('./views/ReportView.vue'), props: true },
    { path: '/switchover', component: () => import('./views/SwitchoverView.vue') },
    { path: '/backups', component: () => import('./views/BackupsView.vue') },
    { path: '/journaux', component: () => import('./views/JournalsView.vue') },
    { path: '/coherence', component: () => import('./views/ConsistencyView.vue') },
    { path: '/audit', component: () => import('./views/AuditView.vue') },
    { path: '/settings', component: () => import('./views/SettingsView.vue') },
  ],
})

createApp(App).use(createPinia()).use(router).mount('#app')
