import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'
import tailwindcss from '@tailwindcss/vite'

export default defineConfig({
  plugins: [react(), tailwindcss()],
  server: {
    port: 5173,
    proxy: {
      // The dev server proxies both REST and WebSocket traffic to the Go
      // backend, so the browser sees one origin and cookies behave exactly as
      // they will in production.
      '/api': {
        target: 'http://127.0.0.1:8080',
        changeOrigin: true,
        ws: true,
      },
    },
  },
  build: {
    outDir: 'dist',
    // Split the heavy, rarely-changing editor and terminal out of the main
    // bundle so a normal page load does not pay for them.
    rollupOptions: {
      output: {
        manualChunks(id: string) {
          if (id.includes('codemirror')) return 'editor'
          if (id.includes('@xterm')) return 'terminal'
          return undefined
        },
      },
    },
  },
})
