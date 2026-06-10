import { defineConfig } from 'vite'
import { svelte } from '@sveltejs/vite-plugin-svelte'

// The built bundle is served by the gateway under /plugins/studio/ui/ and runs
// inside a sandboxed iframe with NO same-origin and a strict CSP (script-src
// 'self', style-src 'self'). Two consequences drive this config:
//
//   - base: './'  — hashed assets must resolve RELATIVE to the serving path
//     (/plugins/studio/ui/) rather than the site root. With an absolute base
//     the iframe would request /assets/* and 404.
//   - everything bundled local, no runtime CDN fetches (the CSP blocks them).
//
// Output overwrites ../ui (the static assets the plugin ships). We commit that
// output; regenerate with `npm install && npm run build` in this folder.
// Strip the `crossorigin` attribute Vite stamps on built <script>/<link> tags.
// In the opaque-origin sandbox it would force a CORS check that same-origin
// asset responses (no Access-Control-Allow-Origin) fail, so assets would not
// load. Removing it makes them ordinary same-origin requests.
function stripCrossorigin() {
  return {
    name: 'strip-crossorigin',
    enforce: 'post',
    transformIndexHtml(html) {
      return html.replace(/\s+crossorigin/g, '')
    },
  }
}

export default defineConfig({
  base: './',
  plugins: [svelte(), stripCrossorigin()],
  build: {
    outDir: '../ui',
    emptyOutDir: true,
    // Keep CSS in its own file (style-src 'self' forbids inline <style>).
    cssCodeSplit: false,
    // The sandboxed iframe has an OPAQUE origin. Vite's default crossorigin
    // attribute on <script>/<link> would force a CORS check that same-origin
    // asset responses (no ACAO header) fail. Strip it so assets load.
    modulePreload: { polyfill: false },
  },
})
