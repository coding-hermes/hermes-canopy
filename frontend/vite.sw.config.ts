import { defineConfig } from 'vite'

// Build config for the Service Worker
// Compiles frontend/sw.ts → dist/sw.js with minimal bundle
export default defineConfig({
  build: {
    outDir: 'dist',
    emptyOutDir: false,
    lib: {
      entry: 'sw.ts',
      name: 'CanopySW',
      formats: ['es'],
      fileName: () => 'sw.js',
    },
    rollupOptions: {
      output: {
        inlineDynamicImports: true,
      },
    },
    minify: false,
    sourcemap: false,
  },
})
