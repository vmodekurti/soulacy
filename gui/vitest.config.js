import { defineConfig } from 'vitest/config'
import { svelte } from '@sveltejs/vite-plugin-svelte'

export default defineConfig({
  // Component tests (the walkthrough overlay, the app shell that carries its
  // anchors) import .svelte files directly, so the test runner needs the same
  // compiler the build uses. Without this, `import App from './App.svelte'`
  // reaches Node as raw markup and every component test fails to collect.
  plugins: [svelte({ hot: false })],
  test: {
    environment: 'node',
    setupFiles: ['./vitest.setup.js'],
    include: ['src/**/*.test.js'],
  },
})
