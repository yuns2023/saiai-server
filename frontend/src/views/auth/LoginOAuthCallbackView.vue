<template>
  <AuthLayout>
    <div class="space-y-6">
      <div class="text-center">
        <h2 class="text-2xl font-bold text-gray-900 dark:text-white">{{ t('auth.oauth.callbackTitle') }}</h2>
        <p class="mt-2 text-sm text-gray-500 dark:text-dark-400">
          {{ isProcessing ? t('auth.oauth.callbackProcessing') : t('auth.oauth.callbackHint') }}
        </p>
      </div>
      <div v-if="needsInvitation" class="space-y-4">
        <p class="text-sm text-gray-700 dark:text-gray-300">{{ t('auth.oauth.invitationRequired') }}</p>
        <input v-model="invitationCode" type="text" class="input w-full" :placeholder="t('auth.invitationCodePlaceholder')" :disabled="isSubmitting" @keyup.enter="submitInvitation" />
        <p v-if="invitationError" class="text-sm text-red-600 dark:text-red-400">{{ invitationError }}</p>
        <button class="btn btn-primary w-full" :disabled="isSubmitting || !invitationCode.trim()" @click="submitInvitation">
          {{ isSubmitting ? t('auth.oauth.completing') : t('auth.oauth.completeRegistration') }}
        </button>
      </div>
      <div v-if="errorMessage" class="rounded-xl border border-red-200 bg-red-50 p-4 dark:border-red-800/50 dark:bg-red-900/20">
        <div class="flex items-start gap-3">
          <Icon name="exclamationCircle" size="md" class="text-red-500" />
          <div class="space-y-2">
            <p class="text-sm text-red-700 dark:text-red-400">{{ errorMessage }}</p>
            <router-link to="/login" class="btn btn-primary">{{ t('auth.oauth.backToLogin') }}</router-link>
          </div>
        </div>
      </div>
    </div>
    <TotpLoginModal
      v-if="requiresTotp"
      ref="totpModalRef"
      :temp-token="totpTempToken"
      :user-email-masked="totpUserEmailMasked"
      @verify="verifyTotp"
      @cancel="cancelTotp"
    />
  </AuthLayout>
</template>

<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { useI18n } from 'vue-i18n'
import { AuthLayout } from '@/components/layout'
import Icon from '@/components/icons/Icon.vue'
import TotpLoginModal from '@/components/auth/TotpLoginModal.vue'
import { useAuthStore, useAppStore } from '@/stores'
import { completeLoginOAuthRegistration } from '@/api/auth'

const route = useRoute()
const router = useRouter()
const { t } = useI18n()
const authStore = useAuthStore()
const appStore = useAppStore()
const isProcessing = ref(true)
const errorMessage = ref('')
const needsInvitation = ref(false)
const registrationToken = ref('')
const provider = ref<'github' | 'google' | ''>('')
const invitationCode = ref('')
const isSubmitting = ref(false)
const invitationError = ref('')
const redirectTo = ref('/dashboard')
const requiresTotp = ref(false)
const totpTempToken = ref('')
const totpUserEmailMasked = ref('')
const totpModalRef = ref<InstanceType<typeof TotpLoginModal> | null>(null)

function fragmentParams(): URLSearchParams {
  const hash = typeof window === 'undefined' ? '' : window.location.hash.replace(/^#/, '')
  return new URLSearchParams(hash)
}

function safeRedirect(path: string | null | undefined): string {
  return path && path.startsWith('/') && !path.startsWith('//') && !path.includes('://') && !/[\r\n]/.test(path) ? path : '/dashboard'
}

async function finishLogin(accessToken: string, refreshToken: string, expiresIn: string): Promise<void> {
  if (refreshToken) localStorage.setItem('refresh_token', refreshToken)
  const seconds = Number.parseInt(expiresIn, 10)
  if (Number.isFinite(seconds)) localStorage.setItem('token_expires_at', String(Date.now() + seconds * 1000))
  await authStore.setToken(accessToken)
  appStore.showSuccess(t('auth.loginSuccess'))
  await router.replace(redirectTo.value)
}

async function submitInvitation(): Promise<void> {
  if (!provider.value || !invitationCode.value.trim()) return
  invitationError.value = ''
  isSubmitting.value = true
  try {
    const tokens = await completeLoginOAuthRegistration(provider.value, registrationToken.value, invitationCode.value.trim())
    await finishLogin(tokens.access_token, tokens.refresh_token, String(tokens.expires_in))
  } catch (error: unknown) {
    const err = error as { message?: string; response?: { data?: { message?: string } } }
    invitationError.value = err.response?.data?.message || err.message || t('auth.oauth.completeRegistrationFailed')
  } finally {
    isSubmitting.value = false
  }
}

async function verifyTotp(code: string): Promise<void> {
  totpModalRef.value?.setVerifying(true)
  try {
    await authStore.login2FA(totpTempToken.value, code)
    appStore.showSuccess(t('auth.loginSuccess'))
    requiresTotp.value = false
    await router.replace(redirectTo.value)
  } catch (error: unknown) {
    const err = error as { message?: string; response?: { data?: { message?: string } } }
    totpModalRef.value?.setError(err.response?.data?.message || err.message || t('profile.totp.loginFailed'))
    totpModalRef.value?.setVerifying(false)
  }
}

async function cancelTotp(): Promise<void> {
  requiresTotp.value = false
  totpTempToken.value = ''
  await router.replace('/login')
}

onMounted(async () => {
  const params = fragmentParams()
  redirectTo.value = safeRedirect(params.get('redirect') || (route.query.redirect as string | undefined))
  const providerParam = params.get('provider')
  if (providerParam === 'github' || providerParam === 'google') provider.value = providerParam
  if (params.get('linked') === 'true' && provider.value) {
    appStore.showSuccess(t('auth.oauth.linkSuccess', { provider: provider.value === 'github' ? 'GitHub' : 'Google' }))
    await router.replace(redirectTo.value)
    return
  }
  const error = params.get('error')
  if (error === 'invitation_required') {
    registrationToken.value = params.get('oauth_registration_token') || ''
    if (!registrationToken.value || !provider.value) errorMessage.value = t('auth.oauth.invalidRegistrationToken')
    else needsInvitation.value = true
    isProcessing.value = false
    return
  }
  if (params.get('requires_2fa') === 'true') {
    totpTempToken.value = params.get('temp_token') || ''
    totpUserEmailMasked.value = params.get('user_email_masked') || ''
    if (!totpTempToken.value) errorMessage.value = t('auth.oauth.callbackMissingToken')
    else requiresTotp.value = true
    isProcessing.value = false
    return
  }
  if (error) {
    errorMessage.value = params.get('error_message') || params.get('error_description') || error
    isProcessing.value = false
    return
  }
  const accessToken = params.get('access_token') || ''
  if (!accessToken) {
    errorMessage.value = t('auth.oauth.callbackMissingToken')
    isProcessing.value = false
    return
  }
  try {
    await finishLogin(accessToken, params.get('refresh_token') || '', params.get('expires_in') || '')
  } catch (error: unknown) {
    errorMessage.value = error instanceof Error ? error.message : t('auth.loginFailed')
    isProcessing.value = false
  }
})
</script>
