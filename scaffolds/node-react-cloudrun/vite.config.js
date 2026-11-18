import { defineConfig } from 'vite';
import react from '@vitejs/plugin-react';

export default defineConfig({
  root: 'web',
  plugins: [react()],
  build: {
    outDir: '../dist/client',
    emptyOutDir: true,
  },
  server: {
    port: 5173,
    // `npm run dev:api` serves the API on 8080; Vite proxies to it.
    proxy: { '/api': 'http://localhost:8080' },
  },
  test: {
    root: '.',
    include: ['test/**/*.test.{js,jsx}'],
    environment: 'node',
  },
});
