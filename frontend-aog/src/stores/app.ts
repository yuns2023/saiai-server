import { defineStore } from 'pinia'
import { computed, ref } from 'vue'
import { authApi } from '@/api'
import type { PublicSettings } from '@/types'

export type ToastKind = 'ok' | 'error' | 'info'
export interface Toast {
  id: number
  kind: ToastKind
  text: string
}

export type ThemePref = 'light' | 'dark' | 'system'
const THEME_KEY = 'theme'
/** Backend default for site_subtitle (setting_service.go); never shown to customers. */
const DEFAULT_BACKEND_SUBTITLE = 'Subscription to API Conversion Platform'

export const useAppStore = defineStore('app', () => {
  const settings = ref<PublicSettings | null>(window.__APP_CONFIG__ ?? null)
  const toasts = ref<Toast[]>([])
  /** No injected config and the API fallback failed: config-driven forms must wait. */
  const settingsFailed = ref(false)
  const theme = ref<ThemePref>(readTheme())
  let toastSeq = 0

  const siteName = computed(() => settings.value?.site_name?.trim() || 'CCYou.dev')
  /** Admin-set tagline; the backend's English placeholder default counts as unset. */
  const siteSubtitle = computed(() => {
    const v = settings.value?.site_subtitle?.trim() || ''
    return v === DEFAULT_BACKEND_SUBTITLE ? '' : v
  })
  const invitationCodeHint = computed(() => settings.value?.invitation_code_hint?.trim() || '')
  const siteLogo = computed(() => settings.value?.site_logo?.trim() || '/_aog/favicon.svg')
  const apiBaseUrl = computed(() => settings.value?.api_base_url?.trim() || window.location.origin)

  async function loadSettings(force = false): Promise<PublicSettings | null> {
    if (settings.value && !force) return settings.value
    try {
      settings.value = await authApi.publicSettings()
      settingsFailed.value = false
    } catch {
      settingsFailed.value = !settings.value
    }
    return settings.value
  }

  function notify(kind: ToastKind, text: string, ms = kind === 'error' ? 6000 : 3200): void {
    const id = ++toastSeq
    toasts.value = [...toasts.value.slice(-3), { id, kind, text }]
    window.setTimeout(() => dismiss(id), ms)
  }

  function dismiss(id: number): void {
    toasts.value = toasts.value.filter((t) => t.id !== id)
  }

  function setTheme(pref: ThemePref): void {
    theme.value = pref
    if (pref === 'system') localStorage.removeItem(THEME_KEY)
    else localStorage.setItem(THEME_KEY, pref)
    applyTheme(pref)
  }

  return {
    settings,
    settingsFailed,
    toasts,
    theme,
    siteName,
    siteSubtitle,
    siteLogo,
    invitationCodeHint,
    apiBaseUrl,
    loadSettings,
    notify,
    dismiss,
    setTheme
  }
})

function readTheme(): ThemePref {
  const saved = localStorage.getItem(THEME_KEY)
  return saved === 'light' || saved === 'dark' ? saved : 'system'
}

/** Shares the classic console's "theme" key so both apps agree. */
export function applyTheme(pref: ThemePref = readTheme()): void {
  const dark = pref === 'dark' || (pref === 'system' && window.matchMedia('(prefers-color-scheme: dark)').matches)
  document.documentElement.classList.toggle('dark', dark)
}
