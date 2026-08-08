<template>
  <div v-if="githubEnabled || googleEnabled" class="space-y-4">
    <div class="grid gap-3" :class="githubEnabled && googleEnabled ? 'sm:grid-cols-2' : ''">
      <button
        v-if="githubEnabled"
        type="button"
        :disabled="disabled"
        class="btn btn-secondary w-full"
        @click="startLogin('github')"
      >
        <svg class="mr-2 h-5 w-5" viewBox="0 0 24 24" aria-hidden="true" fill="currentColor">
          <path d="M12 .7a11.5 11.5 0 0 0-3.64 22.4c.58.1.79-.25.79-.56v-2.23c-3.22.7-3.9-1.37-3.9-1.37-.52-1.34-1.29-1.7-1.29-1.7-1.05-.72.08-.71.08-.71 1.17.08 1.78 1.2 1.78 1.2 1.04 1.77 2.72 1.26 3.38.96.1-.75.4-1.26.74-1.55-2.57-.29-5.27-1.28-5.27-5.7 0-1.26.45-2.29 1.2-3.1-.12-.29-.52-1.47.11-3.06 0 0 .98-.31 3.17 1.18a11 11 0 0 1 5.78 0c2.2-1.49 3.17-1.18 3.17-1.18.63 1.59.23 2.77.11 3.06.75.81 1.2 1.84 1.2 3.1 0 4.43-2.7 5.4-5.28 5.69.42.36.79 1.06.79 2.14v3.18c0 .31.21.67.8.56A11.5 11.5 0 0 0 12 .7Z" />
        </svg>
        {{ t('auth.oauth.signInWith', { provider: 'GitHub' }) }}
      </button>
      <button
        v-if="googleEnabled"
        type="button"
        :disabled="disabled"
        class="btn btn-secondary w-full"
        @click="startLogin('google')"
      >
        <svg class="mr-2 h-5 w-5" viewBox="0 0 24 24" aria-hidden="true">
          <path fill="#4285F4" d="M22.6 12.2c0-.7-.1-1.5-.2-2.2H12v4.3h6a5.2 5.2 0 0 1-2.2 3.3v2.8h3.6c2.1-2 3.2-4.8 3.2-8.2Z" />
          <path fill="#34A853" d="M12 23c3 0 5.5-1 7.4-2.6l-3.6-2.8c-1 .7-2.3 1.1-3.8 1.1-2.9 0-5.4-2-6.3-4.6H2v2.9A11.2 11.2 0 0 0 12 23Z" />
          <path fill="#FBBC05" d="M5.7 14.1A6.8 6.8 0 0 1 5.3 12c0-.7.1-1.4.4-2.1V7H2A11.2 11.2 0 0 0 2 17l3.7-2.9Z" />
          <path fill="#EA4335" d="M12 5.3c1.7 0 3.1.6 4.3 1.7l3.2-3.2A10.8 10.8 0 0 0 12 1 11.2 11.2 0 0 0 2 7l3.7 2.9c.9-2.7 3.4-4.6 6.3-4.6Z" />
        </svg>
        {{ t('auth.oauth.signInWith', { provider: 'Google' }) }}
      </button>
    </div>
    <div class="flex items-center gap-3">
      <div class="h-px flex-1 bg-gray-200 dark:bg-dark-700"></div>
      <span class="text-xs text-gray-500 dark:text-dark-400">{{ t('auth.oauth.orContinue') }}</span>
      <div class="h-px flex-1 bg-gray-200 dark:bg-dark-700"></div>
    </div>
  </div>
</template>

<script setup lang="ts">
import { useRoute } from 'vue-router'
import { useI18n } from 'vue-i18n'

defineProps<{ githubEnabled: boolean; googleEnabled: boolean; disabled?: boolean }>()

const route = useRoute()
const { t } = useI18n()

function startLogin(provider: 'github' | 'google'): void {
  const redirectTo = (route.query.redirect as string) || '/dashboard'
  const apiBase = (import.meta.env.VITE_API_BASE_URL as string | undefined) || '/api/v1'
  window.location.href = `${apiBase.replace(/\/$/, '')}/auth/oauth/${provider}/start?redirect=${encodeURIComponent(redirectTo)}`
}
</script>
