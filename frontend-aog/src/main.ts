import { createApp } from 'vue'
import { createPinia } from 'pinia'
import App from './App.vue'
import { router } from './router'
import { i18n, getLocale } from './i18n'
import { applyTheme, useAppStore } from './stores/app'
import { useAuthStore } from './stores/auth'
import { captureOAuthFragmentAtStartup } from './lib/oauth'
import './styles/main.css'

// Google / GitHub callback: clear tokens from the address bar before anything else runs.
captureOAuthFragmentAtStartup()

applyTheme()
document.documentElement.lang = getLocale() === 'zh' ? 'zh-CN' : 'en'
window.matchMedia('(prefers-color-scheme: dark)').addEventListener('change', () => applyTheme())

const app = createApp(App)
app.use(createPinia())
app.use(i18n)
useAuthStore().restore()
// Injected by the server; when that failed, ask the API before deciding
// which sign-up fields (verification code, invite, Turnstile) to show.
const settingsReady = window.__APP_CONFIG__ ? Promise.resolve() : useAppStore().loadSettings().then(() => undefined)
app.use(router)
settingsReady.finally(() => app.mount('#app'))
