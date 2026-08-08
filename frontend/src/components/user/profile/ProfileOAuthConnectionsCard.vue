<template>
  <div v-if="githubEnabled || googleEnabled" class="card">
    <div class="border-b border-gray-100 px-6 py-4 dark:border-dark-700">
      <h2 class="text-lg font-medium text-gray-900 dark:text-white">{{ t('auth.oauth.connectionsTitle') }}</h2>
      <p class="mt-1 text-sm text-gray-500 dark:text-gray-400">{{ t('auth.oauth.connectionsDescription') }}</p>
    </div>
    <div class="space-y-4 px-6 py-6">
      <div v-if="loading" class="flex justify-center py-4">
        <div class="h-6 w-6 animate-spin rounded-full border-b-2 border-primary-500"></div>
      </div>
      <template v-else>
        <label class="block">
          <span class="mb-1 block text-sm font-medium text-gray-700 dark:text-gray-300">
            {{ requiresTotp ? t('auth.oauth.confirmTotp') : t('auth.oauth.confirmPassword') }}
          </span>
          <input
            v-if="requiresTotp"
            v-model="totpCode"
            type="text"
            inputmode="numeric"
            maxlength="6"
            autocomplete="one-time-code"
            class="input w-full"
            :placeholder="t('auth.oauth.totpPlaceholder')"
          />
          <input
            v-else
            v-model="password"
            type="password"
            autocomplete="current-password"
            class="input w-full"
            :placeholder="t('auth.oauth.passwordPlaceholder')"
          />
        </label>
        <p class="text-xs text-gray-500 dark:text-gray-400">{{ t('auth.oauth.linkReauthHint') }}</p>
        <p v-if="errorMessage" class="text-sm text-red-600 dark:text-red-400">{{ errorMessage }}</p>
        <div class="grid gap-3 sm:grid-cols-2">
          <button
            v-if="githubEnabled"
            type="button"
            class="btn btn-secondary w-full"
            :disabled="submittingProvider !== '' || connections.includes('github')"
            @click="linkProvider('github')"
          >
            {{ connections.includes('github') ? t('auth.oauth.connectedTo', { provider: 'GitHub' }) : t('auth.oauth.linkProvider', { provider: 'GitHub' }) }}
          </button>
          <button
            v-if="googleEnabled"
            type="button"
            class="btn btn-secondary w-full"
            :disabled="submittingProvider !== '' || connections.includes('google')"
            @click="linkProvider('google')"
          >
            {{ connections.includes('google') ? t('auth.oauth.connectedTo', { provider: 'Google' }) : t('auth.oauth.linkProvider', { provider: 'Google' }) }}
          </button>
        </div>
      </template>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { authAPI, totpAPI } from '@/api'

const { t } = useI18n()
const loading = ref(true)
const githubEnabled = ref(false)
const googleEnabled = ref(false)
const totpEnabled = ref(false)
const connections = ref<Array<'github' | 'google'>>([])
const password = ref('')
const totpCode = ref('')
const submittingProvider = ref<'github' | 'google' | ''>('')
const errorMessage = ref('')
const requiresTotp = computed(() => totpEnabled.value)

onMounted(async () => {
  try {
    const [settings, status, linked] = await Promise.all([
      authAPI.getPublicSettings(),
      totpAPI.getStatus(),
      authAPI.getLoginOAuthConnections()
    ])
    githubEnabled.value = settings.github_oauth_enabled
    googleEnabled.value = settings.google_oauth_enabled
    totpEnabled.value = status.feature_enabled && status.enabled
    connections.value = linked
  } catch (error) {
    console.error('Failed to load OAuth connections:', error)
  } finally {
    loading.value = false
  }
})

async function linkProvider(provider: 'github' | 'google'): Promise<void> {
  errorMessage.value = ''
  if (requiresTotp.value && !/^\d{6}$/.test(totpCode.value)) {
    errorMessage.value = t('auth.oauth.invalidTotp')
    return
  }
  if (!requiresTotp.value && !password.value) {
    errorMessage.value = t('auth.oauth.passwordRequired')
    return
  }
  submittingProvider.value = provider
  try {
    const authorizationUrl = await authAPI.startLoginOAuthLink(provider, {
      password: requiresTotp.value ? undefined : password.value,
      totp_code: requiresTotp.value ? totpCode.value : undefined
    })
    window.location.assign(authorizationUrl)
  } catch (error: unknown) {
    const err = error as { message?: string; response?: { data?: { message?: string } } }
    errorMessage.value = err.response?.data?.message || err.message || t('auth.oauth.linkFailed')
    submittingProvider.value = ''
  }
}
</script>
