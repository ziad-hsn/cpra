import { defineConfig } from 'vite';
import react from '@vitejs/plugin-react';

export default defineConfig({
  plugins: [react()],
  base: '/',
  build: {
    outDir: 'dist',
    target: 'es2022',
    sourcemap: false,
  },
  server: {
    port: 5173,
    proxy: {
      '/api': {
        target: 'http://localhost:8060',
        changeOrigin: true,
      },
      '/metrics': {
        target: 'http://localhost:8060',
        changeOrigin: true,
      },
    },
  },
  test: {
    // Keep DOM workers bounded alongside native/race checks on developer hosts.
    // CLI overrides remain available; test assertions and deadlines are unchanged.
    maxWorkers: 2,
    globals: true,
    environment: 'jsdom',
    setupFiles: ['./vitest.setup.ts'],
    css: false,
  },
});
