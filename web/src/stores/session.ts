import { defineStore } from 'pinia'
import { api, type Me, type Site } from '../api'

/** Session, sites accessibles et pharmacie active (rappelée sur chaque écran). */
export const useSession = defineStore('session', {
  state: () => ({
    me: null as Me | null,
    sites: [] as Site[],
    activeSiteId: localStorage.getItem('prm.activeSite') ?? '',
  }),
  getters: {
    activeSite: (s): Site | undefined => s.sites.find((x) => x.id === s.activeSiteId),
    can: (s) => (...roles: string[]) => !!s.me && roles.includes(s.me.role),
  },
  actions: {
    async load() {
      this.me = await api.me()
      this.sites = await api.sites()
      if (this.activeSiteId && !this.sites.some((x) => x.id === this.activeSiteId)) this.setActive('')
    },
    setActive(id: string) {
      this.activeSiteId = id
      localStorage.setItem('prm.activeSite', id)
    },
    async logout() {
      try { await api.logout() } finally { this.me = null; this.sites = [] }
    },
  },
})
