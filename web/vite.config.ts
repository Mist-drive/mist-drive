import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'

export default defineConfig({
  plugins: [react()],
  resolve: {
    alias: { '@shared': new URL('../shared', import.meta.url).pathname },
    dedupe: ['i18next', 'react-i18next'],
  },
  server: {
    host: true,
    port: 5173,
    proxy: {
      '/api': { target: 'http://localhost:3000', changeOrigin: true, ws: true },
      '/auth': { target: 'http://localhost:3000', changeOrigin: true },
      '/health': { target: 'http://localhost:3000', changeOrigin: true },
      // Top-level zip stream route (lives outside /api so no JWT auth runs;
      // authorized by a single-use ticket instead). Without this, dev
      // navigation to /download-zip?ticket=… hits Vite's SPA fallback and
      // just reloads the app. Prod is fine — the Go binary serves it.
      '/download-zip': { target: 'http://localhost:3000', changeOrigin: true },
      // Top-level WebSocket push channel (also outside /api; authenticates
      // via first message). Needs ws:true or the upgrade is silently dropped.
      '/ws': { target: 'http://localhost:3000', changeOrigin: true, ws: true },
    },
  },
})
