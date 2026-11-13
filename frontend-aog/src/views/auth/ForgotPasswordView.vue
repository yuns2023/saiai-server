<script setup lang="ts">
import { computed, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import AuthFrame from '@/components/AuthFrame.vue'
import TurnstileBox from '@/components/TurnstileBox.vue'
import { useAppStore } from '@/stores/app'
import { authApi } from '@/api'
import { errorMessage } from '@/api/client'

const { t } = useI18n()
const app = useAppStore()
const email = ref('')
const busy = ref(false)
const sent = ref(false)
const error = ref('')
const turnstileToken = ref('')
const turnstileKey = computed(() => (app.settings?.turnstile_enabled ? app.settings.turnstile_site_key : ''))
const enabled = computed(() => !!app.settings?.password_reset_enabled)

async function submit(): Promise<void> {
  if (busy.value || !email.value.trim()) return
  if (turnstileKey.value && !turnstileToken.value) {
    error.value = t('auth.turnstileRequired')
    return
  }
  error.value = ''
  busy.value = true
  try {
    await authApi.forgotPassword(email.value.trim(), turnstileToken.value || undefined)
    sent.value = true
  } catch (e) {
    error.value = errorMessage(e, t('auth.forgotFailed'))
  } finally {
    busy.value = false
  }
}
</script>

<template>
  <AuthFrame :title="t('auth.forgotTitle')" :lead="enabled && !sent ? t('auth.forgotLead') : undefined">
    <p v-if="!enabled" class="text-sm text-ink-2">{{ t('auth.resetDisabled') }}</p>
    <p v-else-if="sent" class="text-sm text-ink" role="status">{{ t('auth.forgotSent', { email: email.trim() }) }}</p>
    <form v-else class="space-y-4" novalidate @submit.prevent="submit">
      <label class="block">
        <span class="label">{{ t('auth.email') }}</span>
        <input v-model="email" class="field mt-1" type="email" autocomplete="email" required autofocus />
      </label>
      <TurnstileBox v-if="turnstileKey" :site-key="turnstileKey" @verify="turnstileToken = $event" @expire="turnstileToken = ''" />
      <p v-if="error" class="text-sm text-seal" role="alert">{{ error }}</p>
      <button type="submit" class="btn btn-primary h-10 w-full" :disabled="busy || !email.trim()">{{ t('auth.sendResetLink') }}</button>
    </form>
    <p class="mt-6 border-t border-rule-soft pt-4 text-sm"><RouterLink to="/login">{{ t('auth.backToSignIn') }}</RouterLink></p>
  </AuthFrame>
</template>
