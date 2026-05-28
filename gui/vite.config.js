import { defineConfig } from 'vite'
import { svelte } from '@sveltejs/vite-plugin-svelte'

export default defineConfig({
  plugins: [svelte()],

  // During 'npm run dev', proxy API and WebSocket to the running gateway.
  server: {
    port: 5173,
    proxy: {
      '/api': { target: 'http://localhost:18789', changeOrigin: true },
      '/ws':  { target: 'ws://localhost:18789',  ws: true, changeOrigin: true },
    },
  },

  // Build output goes into the Go embed directory so 'make build' picks it up.
  build: {
    outDir: '../internal/webui/dist',
    emptyOutDir: false,
  },
})
