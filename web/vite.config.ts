import {defineConfig} from 'vite';
import react from '@vitejs/plugin-react';

// The Go server embeds the output and serves it under a strict CSP:
// no inline scripts or styles and no data: URLs.
export default defineConfig({
  plugins: [react()],
  build: {
    outDir: '../internal/web/dist',
    emptyOutDir: true,
    assetsInlineLimit: 0,
    modulePreload: {polyfill: false},
    sourcemap: false,
  },
});
