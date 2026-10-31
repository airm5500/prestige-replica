import { defineConfig } from 'vite'
import vue from '@vitejs/plugin-vue'

// En développement, l'API Quarkus tourne sur 127.0.0.1:8080.
export default defineConfig({
  plugins: [vue()],
  base: './',
  server: {
    proxy: {
      '/api': 'http://127.0.0.1:8080',
      '/j_security_check': 'http://127.0.0.1:8080',
      '/health': 'http://127.0.0.1:8080',
    },
  },
  build: { outDir: 'dist', emptyOutDir: true },
})
