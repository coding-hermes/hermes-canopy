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

const DEV_JWT =
  'eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.' +
  'eyJleHAiOjE4MTY0OTU5ODgsImlhdCI6MTc4NDk1OTk4OCwic3ViIjoiMDAwMDAwMDAt' +
  'MDAwMC0wMDAwLTAwMDAtMDAwMDAwMDAwMDAxIn0.' +
  'AeEXxMtrSsIeoqnuCf-8w8XMaVbB4qIP3oX3vgxXeMI'

// https://vite.dev/config/
export default defineConfig({
  plugins: [react(), tailwindcss()],
  server: {
    proxy: {
      '/api': {
        target: 'http://localhost:8091',
        changeOrigin: true,
        configure(proxy) {
          proxy.on('proxyReq', (proxyReq) => {
            proxyReq.setHeader('Authorization', `Bearer ${DEV_JWT}`)
          })
        },
      },
    },
  },
})
