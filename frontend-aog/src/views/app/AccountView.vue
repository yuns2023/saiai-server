<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { userApi } from '@/api'
import { errorMessage } from '@/api/client'
import { intlLocale } from '@/i18n'
import { dateOnly } from '@/lib/format'
import { useAppStore } from '@/stores/app'
import { useAuthStore } from '@/stores/auth'

const { t } = useI18n()
const app = useAppStore()
const auth = useAuthStore()

const username = ref(auth.user?.username ?? '')
watch(
  () => auth.user?.username,
  (v) => (username.value = v ?? '')
)
const savingName = ref(false)
const nameChanged = computed(() => username.value.trim() !== (auth.user?.username ?? ''))

async function saveName(): Promise<void> {
  if (!nameChanged.value || savingName.value) return
  savingName.value = true
  try {
    await userApi.updateProfile(username.value.trim())
    await auth.refreshUser()
    app.notify('ok', t('common.saved'))
  } catch (e) {
    app.notify('error', errorMessage(e, t('common.saveFailed')))
  } finally {
    savingName.value = false
  }
}

const oldPw = ref('')
const newPw = ref('')
const confirmPw = ref('')
const savingPw = ref(false)
const pwError = ref('')
const pwValid = computed(() => oldPw.value && newPw.value.length >= 6 && newPw.value === confirmPw.value)

async function changePassword(): Promise<void> {
  if (!pwValid.value || savingPw.value) return
  pwError.value = ''
  savingPw.value = true
  try {
    await userApi.changePassword(oldPw.value, newPw.value)
    oldPw.value = newPw.value = confirmPw.value = ''
    app.notify('ok', t('account.passwordChanged'))
  } catch (e) {
    pwError.value = errorMessage(e, t('account.passwordFailed'))
  } finally {
    savingPw.value = false
  }
}
</script>

<template>
  <section aria-labelledby="account-title" class="max-w-2xl">
    <h1 id="account-title" class="text-lg font-semibold tracking-tight">{{ t('account.title') }}</h1>

    <dl class="rule-double mt-5 grid grid-cols-[8rem_1fr] gap-y-2 pb-5 text-sm">
      <dt class="label self-center">{{ t('account.email') }}</dt>
      <dd>{{ auth.user?.email }}</dd>
      <dt class="label self-center">{{ t('account.role') }}</dt>
      <dd>{{ auth.isAdmin ? t('account.roleAdmin') : t('account.roleUser') }}</dd>
      <dt class="label self-center">{{ t('account.concurrency') }}</dt>
      <dd class="num">{{ auth.user?.concurrency ?? '—' }}</dd>
      <dt class="label self-center">{{ t('account.since') }}</dt>
      <dd class="num">{{ dateOnly(auth.user?.created_at, intlLocale()) }}</dd>
    </dl>

    <section class="mt-8" aria-labelledby="profile-title">
      <h2 id="profile-title" class="text-md font-semibold">{{ t('account.profile') }}</h2>
      <form class="mt-3 flex max-w-md items-end gap-2" novalidate @submit.prevent="saveName">
        <label class="block flex-1">
          <span class="label">{{ t('account.username') }}</span>
          <input v-model="username" class="field mt-1" maxlength="100" autocomplete="nickname" />
        </label>
        <button type="submit" class="btn btn-line h-10" :disabled="!nameChanged || savingName">{{ t('common.save') }}</button>
      </form>
    </section>

    <section class="mt-10" aria-labelledby="pw-title">
      <h2 id="pw-title" class="text-md font-semibold">{{ t('account.changePassword') }}</h2>
      <form class="mt-3 max-w-md space-y-4" novalidate @submit.prevent="changePassword">
        <label class="block">
          <span class="label">{{ t('account.currentPassword') }}</span>
          <input v-model="oldPw" class="field mt-1" type="password" autocomplete="current-password" />
        </label>
        <label class="block">
          <span class="label">{{ t('auth.newPassword') }}</span>
          <input v-model="newPw" class="field mt-1" type="password" autocomplete="new-password" />
          <span class="mt-1 block text-xs text-ink-3">{{ t('auth.passwordRule') }}</span>
        </label>
        <label class="block">
          <span class="label">{{ t('auth.confirmPassword') }}</span>
          <input
            v-model="confirmPw"
            class="field mt-1"
            type="password"
            autocomplete="new-password"
            :aria-invalid="confirmPw && confirmPw !== newPw ? 'true' : undefined"
          />
          <span v-if="confirmPw && confirmPw !== newPw" class="mt-1 block text-xs text-seal">{{ t('auth.passwordMismatch') }}</span>
        </label>
        <p v-if="pwError" class="text-sm text-seal" role="alert">{{ pwError }}</p>
        <button type="submit" class="btn btn-primary" :disabled="!pwValid || savingPw">{{ t('account.updatePassword') }}</button>
      </form>
    </section>
  </section>
</template>
