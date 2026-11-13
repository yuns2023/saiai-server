<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { useI18n } from 'vue-i18n'
import AuthFrame from '@/components/AuthFrame.vue'
import TurnstileBox from '@/components/TurnstileBox.vue'
import { useAuthStore } from '@/stores/auth'
import { useAppStore } from '@/stores/app'
import { needs2FA } from '@/api'
import { errorMessage } from '@/api/client'
import { postLoginTarget } from '@/lib/redirect'

const { t } = useI18n()
const auth = useAuthStore()
const app = useAppStore()
const route = useRoute()
const router = useRouter()

const email = ref('')
const password = ref('')
const code = ref('')
const tempToken = ref('')
const maskedEmail = ref('')
const turnstileToken = ref('')
const busy = ref(false)
const error = ref('')
const expired = ref(false)

const turnstileKey = computed(() => (app.settings?.turnstile_enabled ? app.settings.turnstile_site_key : ''))
const canRegister = computed(() => app.settings?.registration_enabled !== false)
const canReset = computed(() => !!app.settings?.password_reset_enabled)
const turnstile = ref<InstanceType<typeof TurnstileBox> | null>(null)

onMounted(() => {
  expired.value = sessionStorage.getItem('auth_expired') === '1'
  sessionStorage.removeItem('auth_expired')
})

async function go(): Promise<void> {
  const target = postLoginTarget(route.query.redirect, auth.isAdmin)
  if (target.fullPage) window.location.assign(target.path)
  else await router.replace(target.path)
}

async function submit(): Promise<void> {
  if (busy.value) return
  error.value = ''
  if (turnstileKey.value && !turnstileToken.value) {
    error.value = t('auth.turnstileRequired')
    return
  }
  busy.value = true
  try {
    const result = await auth.login(email.value.trim(), password.value, turnstileToken.value || undefined)
    if (needs2FA(result)) {
      tempToken.value = result.temp_token
      maskedEmail.value = result.user_email_masked ?? ''
      return
    }
    await go()
  } catch (e) {
    error.value = errorMessage(e, t('auth.loginFailed'))
    turnstileToken.value = ''
    turnstile.value?.reset()
  } finally {
    busy.value = false
  }
}

async function submitCode(): Promise<void> {
  if (busy.value) return
  error.value = ''
  busy.value = true
  try {
    await auth.login2FA(tempToken.value, code.value.trim())
    await go()
  } catch (e) {
    error.value = errorMessage(e, t('auth.codeFailed'))
  } finally {
    busy.value = false
  }
}
</script>

<template>
  <AuthFrame :title="tempToken ? t('auth.twoFactorTitle') : t('auth.signIn')" :lead="tempToken ? t('auth.twoFactorLead', { email: maskedEmail }) : undefined">
    <p v-if="expired && !tempToken" class="mb-4 rounded border border-warn/40 bg-warn-wash px-3 py-2 text-sm text-warn" role="status">
      {{ t('auth.sessionExpired') }}
    </p>

    <form v-if="!tempToken" class="space-y-4" novalidate @submit.prevent="submit">
      <label class="block">
        <span class="label">{{ t('auth.email') }}</span>
        <input v-model="email" class="field mt-1" type="email" autocomplete="email" inputmode="email" required autofocus />
      </label>
      <label class="block">
        <span class="flex items-baseline justify-between">
          <span class="label">{{ t('auth.password') }}</span>
          <RouterLink v-if="canReset" to="/forgot-password" class="text-xs">{{ t('auth.forgot') }}</RouterLink>
        </span>
        <input v-model="password" class="field mt-1" type="password" autocomplete="current-password" required />
      </label>
      <TurnstileBox v-if="turnstileKey" ref="turnstile" :site-key="turnstileKey" @verify="turnstileToken = $event" @expire="turnstileToken = ''" />
      <p v-if="error" class="text-sm text-seal" role="alert">{{ error }}</p>
      <button type="submit" class="btn btn-primary h-10 w-full" :disabled="busy || !email || !password">
        {{ busy ? t('auth.signingIn') : t('auth.signIn') }}
      </button>
    </form>

    <form v-else class="space-y-4" novalidate @submit.prevent="submitCode">
      <label class="block">
        <span class="label">{{ t('auth.twoFactorCode') }}</span>
        <input v-model="code" class="field num mt-1 tracking-[0.3em]" inputmode="numeric" autocomplete="one-time-code" maxlength="6" required autofocus />
      </label>
      <p v-if="error" class="text-sm text-seal" role="alert">{{ error }}</p>
      <button type="submit" class="btn btn-primary h-10 w-full" :disabled="busy || code.trim().length < 6">{{ t('auth.verify') }}</button>
      <button type="button" class="btn btn-quiet w-full" @click="tempToken = ''; code = ''">{{ t('common.back') }}</button>
    </form>

    <p v-if="canRegister && !tempToken" class="mt-6 border-t border-rule-soft pt-4 text-sm text-ink-2">
      {{ t('auth.noAccount') }}
      <RouterLink :to="{ path: '/register', query: route.query.redirect ? { redirect: route.query.redirect } : {} }">{{ t('auth.register') }}</RouterLink>
    </p>
  </AuthFrame>
</template>
