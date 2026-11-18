import { defineConfig } from 'vite';
import react from '@vitejs/plugin-react';

export default defineConfig({
  plugins: [react()],
  server: { proxy: { '/api': 'http://localhost:8080' } },
  build: {
    // .vite/manifest.json lists every chunk; the service worker uses it to precache the app for offline collectors
    manifest: true,
    chunkSizeWarningLimit: 700,
  },
});
