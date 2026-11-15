<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { useI18n } from 'vue-i18n'
import AuthFrame from '@/components/AuthFrame.vue'
import { useAuthStore } from '@/stores/auth'
import { errorMessage } from '@/api/client'
import { postLoginTarget } from '@/lib/redirect'
import { isSocialProvider, parseOAuthFragment, takeOAuthFragment, type OAuthCallbackResult } from '@/lib/oauth'

const { t, te } = useI18n()
const auth = useAuthStore()
const route = useRoute()
const router = useRouter()

type Stage = 'working' | 'twoFactor' | 'invitation' | 'error'

const provider = computed(() => (isSocialProvider(route.params.provider) ? route.params.provider : null))
const providerName = computed(() => (provider.value === 'github' ? 'GitHub' : 'Google'))
const stage = ref<Stage>('working')
const errorText = ref('')
const formError = ref('')
const busy = ref(false)
const code = ref('')
const invite = ref('')
let redirect: string | null = null
let tempToken = ''
let pendingToken = ''

const title = computed(() => {
  if (stage.value === 'twoFactor') return t('auth.twoFactorTitle')
  if (stage.value === 'invitation') return t('auth.oauthInviteTitle')
  if (stage.value === 'error') return t('auth.oauthFailedTitle')
  return t('auth.oauthWorking', { provider: providerName.value })
})
const lead = computed(() => {
  if (stage.value === 'twoFactor') return t('auth.oauthTwoFactorLead')
  if (stage.value === 'invitation') return t('auth.oauthInviteLead', { provider: providerName.value })
  return undefined
})

function describeError(result: Extract<OAuthCallbackResult, { kind: 'error' }>): string {
  const params = { provider: providerName.value }
  if (result.code === 'login_failed' && result.reason && te(`auth.oauthErrors.${result.reason}`)) {
    return t(`auth.oauthErrors.${result.reason}`, params)
  }
  if (te(`auth.oauthErrors.${result.code}`)) return t(`auth.oauthErrors.${result.code}`, params)
  return t('auth.oauthErrors.generic', params)
}

async function go(): Promise<void> {
  const target = postLoginTarget(redirect, auth.isAdmin)
  if (target.fullPage) window.location.assign(target.path)
  else await router.replace(target.path)
}

onMounted(async () => {
  // Read the hand-over once and drop it from the address bar and history right away.
  const result = parseOAuthFragment(takeOAuthFragment())
  if (!provider.value) {
    stage.value = 'error'
    errorText.value = t('auth.oauthErrors.generic', { provider: providerName.value })
    return
  }
  switch (result.kind) {
    case 'tokens':
      redirect = result.redirect
      try {
        await auth.adoptTokens(result.accessToken, result.refreshToken, result.expiresIn)
        await go()
      } catch (e) {
        stage.value = 'error'
        errorText.value = errorMessage(e, t('auth.oauthErrors.generic', { provider: providerName.value }))
      }
      return
    case 'twoFactor':
      redirect = result.redirect
      tempToken = result.tempToken
      stage.value = 'twoFactor'
      return
    case 'invitation':
      redirect = result.redirect
      pendingToken = result.pendingToken
      stage.value = 'invitation'
      return
    default:
      stage.value = 'error'
      errorText.value = describeError(result)
  }
})

async function submitCode(): Promise<void> {
  if (busy.value) return
  formError.value = ''
  busy.value = true
  try {
    await auth.login2FA(tempToken, code.value.trim())
    await go()
  } catch (e) {
    formError.value = errorMessage(e, t('auth.codeFailed'))
  } finally {
    busy.value = false
  }
}

async function submitInvite(): Promise<void> {
  if (busy.value || !provider.value) return
  formError.value = ''
  busy.value = true
  try {
    await auth.completeOAuthRegistration(provider.value, pendingToken, invite.value.trim())
    await go()
  } catch (e) {
    formError.value = errorMessage(e, t('auth.registerFailed'))
  } finally {
    busy.value = false
  }
}
</script>

<template>
  <AuthFrame :title="title" :lead="lead">
    <p v-if="stage === 'working'" class="text-sm text-ink-2" role="status">{{ t('auth.oauthWorkingLead') }}</p>

    <form v-else-if="stage === 'twoFactor'" class="space-y-4" novalidate @submit.prevent="submitCode">
      <label class="block">
        <span class="label">{{ t('auth.twoFactorCode') }}</span>
        <input v-model="code" class="field num mt-1 tracking-[0.3em]" inputmode="numeric" autocomplete="one-time-code" maxlength="6" required autofocus />
      </label>
      <p v-if="formError" class="text-sm text-seal" role="alert">{{ formError }}</p>
      <button type="submit" class="btn btn-primary h-10 w-full" :disabled="busy || code.trim().length < 6">{{ t('auth.verify') }}</button>
    </form>

    <form v-else-if="stage === 'invitation'" class="space-y-4" novalidate @submit.prevent="submitInvite">
      <label class="block">
        <span class="label">{{ t('auth.inviteCode') }}</span>
        <input v-model="invite" class="field mt-1" autocomplete="off" required autofocus />
      </label>
      <p v-if="formError" class="text-sm text-seal" role="alert">{{ formError }}</p>
      <button type="submit" class="btn btn-primary h-10 w-full" :disabled="busy || !invite.trim()">
        {{ busy ? t('auth.creating') : t('auth.createAccount') }}
      </button>
      <p class="text-xs text-ink-3">{{ t('auth.oauthInviteExpiry') }}</p>
    </form>

    <p v-else class="text-sm text-seal" role="alert">{{ errorText }}</p>

    <p v-if="stage !== 'working'" class="mt-6 border-t border-rule-soft pt-4 text-sm text-ink-2">
      <RouterLink to="/login">{{ t('auth.backToSignIn') }}</RouterLink>
    </p>
  </AuthFrame>
</template>
