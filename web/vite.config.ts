import { defineConfig } from 'vite'
import vue from '@vitejs/plugin-vue'

// En développement, l'API Quarkus tourne sur 127.0.0.1:9090.
export default defineConfig({
  plugins: [vue()],
  base: './',
  server: {
    proxy: {
      '/api': 'http://127.0.0.1:9090',
      '/j_security_check': 'http://127.0.0.1:9090',
      '/health': 'http://127.0.0.1:9090',
    },
  },
  build: { outDir: 'dist', emptyOutDir: true },
})
