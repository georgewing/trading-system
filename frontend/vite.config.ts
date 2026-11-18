import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'

// The dev server proxies the Go backend so the browser sees a single origin.
// That keeps the WebSocket handshake simple and avoids CORS in development.
export default defineConfig({
  plugins: [react()],
  server: {
    port: 5173,
    proxy: {
      '/api': {
        target: 'http://127.0.0.1:8090',
        changeOrigin: true,
      },
      '/ws': {
        target: 'ws://127.0.0.1:8090',
        ws: true,
      },
    },
  },
})
