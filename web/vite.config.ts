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
        // Overridable so a second checkout or a rebuilt backend can be tested
        // without stealing port 8080 from a backend already running.
        target: process.env.ORRERY_API ?? 'http://127.0.0.1:8080',
        changeOrigin: true,
        ws: true,
      },
    },
  },
  build: {
    outDir: 'dist',
    // No manualChunks: the editor and terminal are reached through dynamic
    // imports (see ResourceDetail and the create route), so the bundler splits
    // them out on those boundaries by itself. Forcing them into named chunks
    // by module id used to sweep shared modules in alongside them, which made
    // the entry import the editor chunk statically and preload ~450K nobody
    // had asked for.
  },
})
