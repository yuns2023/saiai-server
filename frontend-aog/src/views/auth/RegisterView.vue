<script setup lang="ts">
import { computed, onUnmounted, ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { useI18n } from 'vue-i18n'
import AuthFrame from '@/components/AuthFrame.vue'
import TurnstileBox from '@/components/TurnstileBox.vue'
import { useAuthStore } from '@/stores/auth'
import { useAppStore } from '@/stores/app'
import { authApi } from '@/api'
import { errorMessage } from '@/api/client'
import { postLoginTarget } from '@/lib/redirect'

const { t } = useI18n()
const auth = useAuthStore()
const app = useAppStore()
const route = useRoute()
const router = useRouter()

const s = computed(() => app.settings)
const enabled = computed(() => s.value?.registration_enabled !== false)
const needCode = computed(() => !!s.value?.email_verify_enabled)
const needInvite = computed(() => !!s.value?.invitation_code_enabled)
const promoOn = computed(() => !!s.value?.promo_code_enabled)
const suffixes = computed(() => s.value?.registration_email_suffix_whitelist ?? [])
const turnstileKey = computed(() => (s.value?.turnstile_enabled ? s.value.turnstile_site_key : ''))

const email = ref('')
const password = ref('')
const verifyCode = ref('')
const invite = ref(typeof route.query.invite === 'string' ? route.query.invite : '')
const promo = ref(typeof route.query.promo === 'string' ? route.query.promo : '')
const turnstileToken = ref('')
const turnstile = ref<InstanceType<typeof TurnstileBox> | null>(null)
const busy = ref(false)
const sending = ref(false)
const countdown = ref(0)
const error = ref('')
const notice = ref('')
let tick: number | undefined

const emailOk = computed(() => {
  const v = email.value.trim().toLowerCase()
  if (!/^[^\s@]+@[^\s@]+\.[^\s@]+$/.test(v)) return false
  return suffixes.value.length === 0 || suffixes.value.some((sfx) => v.endsWith(sfx.toLowerCase()))
})
const passwordOk = computed(() => password.value.length >= 6)
const ready = computed(
  () => emailOk.value && passwordOk.value && (!needCode.value || verifyCode.value.trim()) && (!needInvite.value || invite.value.trim())
)

function startCountdown(seconds: number): void {
  countdown.value = seconds
  window.clearInterval(tick)
  tick = window.setInterval(() => {
    countdown.value -= 1
    if (countdown.value <= 0) window.clearInterval(tick)
  }, 1000)
}
onUnmounted(() => window.clearInterval(tick))

async function sendCode(): Promise<void> {
  if (!emailOk.value || sending.value || countdown.value > 0) return
  if (turnstileKey.value && !turnstileToken.value) {
    error.value = t('auth.turnstileRequired')
    return
  }
  error.value = ''
  sending.value = true
  try {
    const res = await authApi.sendVerifyCode(email.value.trim(), turnstileToken.value || undefined)
    notice.value = t('auth.codeSent', { email: email.value.trim() })
    startCountdown(res.countdown || 60)
  } catch (e) {
    error.value = errorMessage(e, t('auth.sendCodeFailed'))
  } finally {
    turnstileToken.value = ''
    turnstile.value?.reset()
    sending.value = false
  }
}

async function submit(): Promise<void> {
  if (!ready.value || busy.value) return
  // With email verification the backend already checked Turnstile when the code was sent.
  if (turnstileKey.value && !needCode.value && !turnstileToken.value) {
    error.value = t('auth.turnstileRequired')
    return
  }
  error.value = ''
  busy.value = true
  try {
    await auth.register({
      email: email.value.trim(),
      password: password.value,
      verify_code: needCode.value ? verifyCode.value.trim() : undefined,
      invitation_code: invite.value.trim() || undefined,
      promo_code: promoOn.value && promo.value.trim() ? promo.value.trim() : undefined,
      turnstile_token: turnstileToken.value || undefined
    })
    const target = postLoginTarget(route.query.redirect, auth.isAdmin)
    if (target.fullPage) window.location.assign(target.path)
    else await router.replace(target.path)
  } catch (e) {
    error.value = errorMessage(e, t('auth.registerFailed'))
    turnstileToken.value = ''
    turnstile.value?.reset()
  } finally {
    busy.value = false
  }
}
</script>

<template>
  <AuthFrame :title="t('auth.register')" :lead="enabled ? t('auth.registerLead') : undefined">
    <p v-if="!enabled" class="text-sm text-ink-2">{{ t('auth.registerClosed') }}</p>

    <form v-else class="space-y-4" novalidate @submit.prevent="submit">
      <label class="block">
        <span class="label">{{ t('auth.email') }}</span>
        <input
          v-model="email"
          class="field mt-1"
          type="email"
          autocomplete="email"
          inputmode="email"
          :aria-invalid="email && !emailOk ? 'true' : undefined"
          required
          autofocus
        />
        <span v-if="suffixes.length" class="mt-1 block text-xs text-ink-3">{{ t('auth.emailSuffixHint', { list: suffixes.join(' / ') }) }}</span>
      </label>

      <label v-if="needCode" class="block">
        <span class="label">{{ t('auth.verifyCode') }}</span>
        <span class="mt-1 flex gap-2">
          <input v-model="verifyCode" class="field num" inputmode="numeric" autocomplete="one-time-code" maxlength="8" required />
          <button type="button" class="btn btn-line h-10 shrink-0" :disabled="!emailOk || sending || countdown > 0" @click="sendCode">
            {{ countdown > 0 ? t('auth.resendIn', { s: countdown }) : sending ? t('auth.sending') : t('auth.sendCode') }}
          </button>
        </span>
      </label>

      <label class="block">
        <span class="label">{{ t('auth.password') }}</span>
        <input
          v-model="password"
          class="field mt-1"
          type="password"
          autocomplete="new-password"
          :aria-invalid="password && !passwordOk ? 'true' : undefined"
          required
        />
        <span class="mt-1 block text-xs text-ink-3">{{ t('auth.passwordRule') }}</span>
      </label>

      <label v-if="needInvite" class="block">
        <span class="label">{{ t('auth.inviteCode') }}</span>
        <input v-model="invite" class="field mt-1" autocomplete="off" required />
      </label>

      <label v-if="promoOn" class="block">
        <span class="label">{{ t('auth.promoCode') }} <span class="text-ink-3">· {{ t('common.optional') }}</span></span>
        <input v-model="promo" class="field mt-1" autocomplete="off" />
      </label>

      <TurnstileBox v-if="turnstileKey" ref="turnstile" :site-key="turnstileKey" @verify="turnstileToken = $event" @expire="turnstileToken = ''" />
      <p v-if="notice && !error" class="text-sm text-ok" role="status">{{ notice }}</p>
      <p v-if="error" class="text-sm text-seal" role="alert">{{ error }}</p>
      <button type="submit" class="btn btn-primary h-10 w-full" :disabled="busy || !ready">
        {{ busy ? t('auth.creating') : t('auth.createAccount') }}
      </button>
    </form>

    <p class="mt-6 border-t border-rule-soft pt-4 text-sm text-ink-2">
      {{ t('auth.haveAccount') }} <RouterLink to="/login">{{ t('auth.signIn') }}</RouterLink>
    </p>
  </AuthFrame>
</template>
