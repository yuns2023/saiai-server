<script setup lang="ts">
import { computed, ref } from 'vue'
import { useRoute } from 'vue-router'
import { useI18n } from 'vue-i18n'
import AuthFrame from '@/components/AuthFrame.vue'
import { authApi } from '@/api'
import { errorMessage } from '@/api/client'

const { t } = useI18n()
const route = useRoute()
const email = computed(() => (typeof route.query.email === 'string' ? route.query.email : ''))
const token = computed(() => (typeof route.query.token === 'string' ? route.query.token : ''))
const password = ref('')
const confirm = ref('')
const busy = ref(false)
const done = ref(false)
const error = ref('')
const valid = computed(() => password.value.length >= 6 && password.value === confirm.value)

async function submit(): Promise<void> {
  if (!valid.value || busy.value) return
  error.value = ''
  busy.value = true
  try {
    await authApi.resetPassword(email.value, token.value, password.value)
    done.value = true
  } catch (e) {
    error.value = errorMessage(e, t('auth.resetFailed'))
  } finally {
    busy.value = false
  }
}
</script>

<template>
  <AuthFrame :title="t('auth.resetTitle')" :lead="email && token && !done ? t('auth.resetLead', { email }) : undefined">
    <p v-if="!email || !token" class="text-sm text-ink-2">{{ t('auth.resetLinkInvalid') }}</p>
    <div v-else-if="done" class="space-y-4" role="status">
      <p class="text-sm">{{ t('auth.resetDone') }}</p>
      <RouterLink to="/login" class="btn btn-primary h-10 w-full no-underline">{{ t('auth.signIn') }}</RouterLink>
    </div>
    <form v-else class="space-y-4" novalidate @submit.prevent="submit">
      <label class="block">
        <span class="label">{{ t('auth.newPassword') }}</span>
        <input v-model="password" class="field mt-1" type="password" autocomplete="new-password" required autofocus />
        <span class="mt-1 block text-xs text-ink-3">{{ t('auth.passwordRule') }}</span>
      </label>
      <label class="block">
        <span class="label">{{ t('auth.confirmPassword') }}</span>
        <input
          v-model="confirm"
          class="field mt-1"
          type="password"
          autocomplete="new-password"
          :aria-invalid="confirm && confirm !== password ? 'true' : undefined"
          required
        />
        <span v-if="confirm && confirm !== password" class="mt-1 block text-xs text-seal">{{ t('auth.passwordMismatch') }}</span>
      </label>
      <p v-if="error" class="text-sm text-seal" role="alert">{{ error }}</p>
      <button type="submit" class="btn btn-primary h-10 w-full" :disabled="busy || !valid">{{ t('auth.setPassword') }}</button>
    </form>
  </AuthFrame>
</template>
