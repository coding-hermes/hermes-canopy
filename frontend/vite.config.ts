import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'
import tailwindcss from '@tailwindcss/vite'

// ─── Dev JWT auto-injection ───────────────────────────────────────────────
//
// In dev mode the Vite dev server proxies /api requests to the backend.
// This pre-generated JWT is injected so the backend's AuthMiddleware (HS256)
// sees a valid Bearer token instead of returning TOKEN_MISSING.
//
// Dev secret:  "dev-secret-change-me"  (config.FromEnv default)
// Dev user ID: 00000000-0000-0000-0000-000000000001
// Expires:     far future (365-day rolling window from generation time)
//
// Production deployments MUST set VITE_API_BASE_URL (the proxy + this token
// are only active inside `vite dev`).  The production backend must use a
// real JWT secret; this dev secret never leaves the dev environment.

const DEV_JWT_DEFAULT =
  'eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.' +
  'eyJleHAiOjE4MTY0OTU5ODgsImlhdCI6MTc4NDk1OTk4OCwic3ViIjoiMDAwMDAwMDAt' +
  'MDAwMC0wMDAwLTAwMDAtMDAwMDAwMDAwMDAxIn0.' +
  'AeEXxMtrSsIeoqnuCf-8w8XMaVbB4qIP3oX3vgxXeMI'

const DEV_JWT = process.env.VITE_DEV_JWT || DEV_JWT_DEFAULT
const API_URL = process.env.VITE_API_URL || 'http://localhost:8091'

// ─── pdf.js host-bundle assets (SPEC-PL-02 §9.1, PL02-P9) ─────────────────
//
// Copies pdf.min.mjs + pdf.worker.min.mjs from the LOCKED pdfjs-dist npm
// dependency into public/pdfjs/ on every dev/build start. public/ is served
// verbatim by the dev server and copied verbatim into dist by the build, so
// /pdfjs/*.mjs are local same-origin build outputs — no CDN, no network
// dependency, and no vendored blob in the repo (public/pdfjs/ is
// gitignored; bytes always come from node_modules).
import { copyFileSync, mkdirSync } from 'node:fs'
import { join } from 'node:path'

function copyPdfjsAssets() {
  const outDir = join(__dirname, 'public', 'pdfjs')
  mkdirSync(outDir, { recursive: true })
  for (const file of ['pdf.min.mjs', 'pdf.worker.min.mjs']) {
    copyFileSync(
      join(__dirname, 'node_modules', 'pdfjs-dist', 'build', file),
      join(outDir, file),
    )
  }
}

copyPdfjsAssets()

// The baseline production entry was 1,031,244 bytes minified (289.10 kB gzip).
// Keep the initial chunk below Vite's 500 kB warning budget by splitting the
// measured heavyweight vendors instead of hiding the warning with a larger limit.
const productionCodeSplitting = {
  groups: [
    {
      name: 'react-vendor',
      test: /node_modules[\\/](?:react|react-dom|react-router-dom)[\\/]/,
      priority: 20,
    },
    {
      name: 'graph-vendor',
      test: /node_modules[\\/](?:@xyflow[\\/](?:react|system)|d3-hierarchy|dagre|y-indexeddb|yjs)[\\/]/,
      priority: 15,
    },
    {
      name: 'pdfjs-vendor',
      test: /node_modules[\\/]pdfjs-dist[\\/]/,
      priority: 20,
    },
    {
      name: 'icons-vendor',
      test: /node_modules[\\/]lucide-react[\\/]/,
      priority: 10,
    },
  ],
}

// https://vite.dev/config/
export default defineConfig({
  plugins: [react(), tailwindcss()],
  build: {
    rolldownOptions: {
      output: {
        codeSplitting: productionCodeSplitting,
      },
    },
  },
  server: {
    // LAN/Tailscale access (Bane 08-22): bind all interfaces, no tunnel host
    // allowance needed anymore (cloudflare quick tunnel retired).
    host: '0.0.0.0',
    proxy: {
      '/api': {
        target: API_URL,
        changeOrigin: true,
        configure(proxy) {
          proxy.on('proxyReq', (proxyReq) => {
            proxyReq.setHeader('Authorization', `Bearer ${DEV_JWT}`)
          })
        },
      },
      '/health': {
        target: API_URL,
        changeOrigin: true,
      },
    },
  },
})
