// vite.config.js
import { defineConfig } from "file:///sessions/hopeful-quirky-curie/mnt/agenticai/clawstack/gui/node_modules/vite/dist/node/index.js";
import { svelte } from "file:///sessions/hopeful-quirky-curie/mnt/agenticai/clawstack/gui/node_modules/@sveltejs/vite-plugin-svelte/src/index.js";
var vite_config_default = defineConfig({
  plugins: [svelte()],
  // During 'npm run dev', proxy API and WebSocket to the running gateway.
  server: {
    port: 5173,
    proxy: {
      "/api": { target: "http://localhost:18789", changeOrigin: true },
      "/ws": { target: "ws://localhost:18789", ws: true, changeOrigin: true }
    }
  },
  // Build output goes into the Go embed directory so 'make build' picks it up.
  build: {
    outDir: "../internal/webui/dist",
    emptyOutDir: true
  }
});
export {
  vite_config_default as default
};
//# sourceMappingURL=data:application/json;base64,ewogICJ2ZXJzaW9uIjogMywKICAic291cmNlcyI6IFsidml0ZS5jb25maWcuanMiXSwKICAic291cmNlc0NvbnRlbnQiOiBbImNvbnN0IF9fdml0ZV9pbmplY3RlZF9vcmlnaW5hbF9kaXJuYW1lID0gXCIvc2Vzc2lvbnMvaG9wZWZ1bC1xdWlya3ktY3VyaWUvbW50L2FnZW50aWNhaS9jbGF3c3RhY2svZ3VpXCI7Y29uc3QgX192aXRlX2luamVjdGVkX29yaWdpbmFsX2ZpbGVuYW1lID0gXCIvc2Vzc2lvbnMvaG9wZWZ1bC1xdWlya3ktY3VyaWUvbW50L2FnZW50aWNhaS9jbGF3c3RhY2svZ3VpL3ZpdGUuY29uZmlnLmpzXCI7Y29uc3QgX192aXRlX2luamVjdGVkX29yaWdpbmFsX2ltcG9ydF9tZXRhX3VybCA9IFwiZmlsZTovLy9zZXNzaW9ucy9ob3BlZnVsLXF1aXJreS1jdXJpZS9tbnQvYWdlbnRpY2FpL2NsYXdzdGFjay9ndWkvdml0ZS5jb25maWcuanNcIjtpbXBvcnQgeyBkZWZpbmVDb25maWcgfSBmcm9tICd2aXRlJ1xuaW1wb3J0IHsgc3ZlbHRlIH0gZnJvbSAnQHN2ZWx0ZWpzL3ZpdGUtcGx1Z2luLXN2ZWx0ZSdcblxuZXhwb3J0IGRlZmF1bHQgZGVmaW5lQ29uZmlnKHtcbiAgcGx1Z2luczogW3N2ZWx0ZSgpXSxcblxuICAvLyBEdXJpbmcgJ25wbSBydW4gZGV2JywgcHJveHkgQVBJIGFuZCBXZWJTb2NrZXQgdG8gdGhlIHJ1bm5pbmcgZ2F0ZXdheS5cbiAgc2VydmVyOiB7XG4gICAgcG9ydDogNTE3MyxcbiAgICBwcm94eToge1xuICAgICAgJy9hcGknOiB7IHRhcmdldDogJ2h0dHA6Ly9sb2NhbGhvc3Q6MTg3ODknLCBjaGFuZ2VPcmlnaW46IHRydWUgfSxcbiAgICAgICcvd3MnOiAgeyB0YXJnZXQ6ICd3czovL2xvY2FsaG9zdDoxODc4OScsICB3czogdHJ1ZSwgY2hhbmdlT3JpZ2luOiB0cnVlIH0sXG4gICAgfSxcbiAgfSxcblxuICAvLyBCdWlsZCBvdXRwdXQgZ29lcyBpbnRvIHRoZSBHbyBlbWJlZCBkaXJlY3Rvcnkgc28gJ21ha2UgYnVpbGQnIHBpY2tzIGl0IHVwLlxuICBidWlsZDoge1xuICAgIG91dERpcjogJy4uL2ludGVybmFsL3dlYnVpL2Rpc3QnLFxuICAgIGVtcHR5T3V0RGlyOiB0cnVlLFxuICB9LFxufSlcbiJdLAogICJtYXBwaW5ncyI6ICI7QUFBZ1csU0FBUyxvQkFBb0I7QUFDN1gsU0FBUyxjQUFjO0FBRXZCLElBQU8sc0JBQVEsYUFBYTtBQUFBLEVBQzFCLFNBQVMsQ0FBQyxPQUFPLENBQUM7QUFBQTtBQUFBLEVBR2xCLFFBQVE7QUFBQSxJQUNOLE1BQU07QUFBQSxJQUNOLE9BQU87QUFBQSxNQUNMLFFBQVEsRUFBRSxRQUFRLDBCQUEwQixjQUFjLEtBQUs7QUFBQSxNQUMvRCxPQUFRLEVBQUUsUUFBUSx3QkFBeUIsSUFBSSxNQUFNLGNBQWMsS0FBSztBQUFBLElBQzFFO0FBQUEsRUFDRjtBQUFBO0FBQUEsRUFHQSxPQUFPO0FBQUEsSUFDTCxRQUFRO0FBQUEsSUFDUixhQUFhO0FBQUEsRUFDZjtBQUNGLENBQUM7IiwKICAibmFtZXMiOiBbXQp9Cg==
