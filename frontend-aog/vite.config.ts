import { defineConfig, loadEnv, type Plugin } from 'vite'
import vue from '@vitejs/plugin-vue'
import { resolve } from 'path'

/**
 * Dev only: inject public settings the same way the Go server does in
 * production, so the dev page renders the configured site name immediately.
 */
function injectPublicSettings(backendUrl: string): Plugin {
  return {
    name: 'aog-inject-public-settings',
    apply: 'serve',
    transformIndexHtml: {
      order: 'pre',
      async handler(html) {
        try {
          const response = await fetch(`${backendUrl}/api/v1/settings/public`, {
            signal: AbortSignal.timeout(2000)
          })
          if (response.ok) {
            const data = await response.json()
            if (data.code === 0 && data.data) {
              const script = `<script>window.__APP_CONFIG__=${JSON.stringify(data.data)};window.__SAIAI_USER_UI__="aog";</script>`
              return html.replace('</head>', `${script}\n</head>`)
            }
          }
        } catch (e) {
          console.warn('[vite] public settings unavailable, falling back to the API:', (e as Error).message)
        }
        return html
      }
    }
  }
}

export default defineConfig(({ mode }) => {
  const env = loadEnv(mode, process.cwd(), '')
  const backendUrl = env.VITE_DEV_PROXY_TARGET || 'http://localhost:8080'
  const devPort = Number(env.VITE_DEV_PORT || 3100)

  return {
    plugins: [vue(), injectPublicSettings(backendUrl)],
    resolve: {
      alias: {
        '@': resolve(__dirname, 'src'),
        // Runtime-only vue-i18n: no eval, works under the server's CSP.
        'vue-i18n': 'vue-i18n/dist/vue-i18n.runtime.esm-bundler.js'
      }
    },
    define: {
      __INTLIFY_JIT_COMPILATION__: true
    },
    build: {
      // Served next to the classic build: every emitted file lives under
      // /_aog/ so it can never shadow the classic console's /assets or /logo.png.
      outDir: '../backend/internal/web/dist-aog',
      emptyOutDir: true,
      assetsDir: '_aog'
    },
    server: {
      host: '0.0.0.0',
      port: devPort,
      proxy: {
        '/api': { target: backendUrl, changeOrigin: true },
        '/saiai-cli': { target: backendUrl, changeOrigin: true },
        '/admin': { target: backendUrl, changeOrigin: true },
        '/assets': { target: backendUrl, changeOrigin: true }
      }
    }
  }
})
