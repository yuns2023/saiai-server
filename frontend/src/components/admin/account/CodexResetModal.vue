<template>
  <BaseDialog :show="show" :title="t('admin.accounts.codexReset.title')" :close-on-escape="!busy" @close="close">
    <p class="mb-4 text-sm text-gray-600 dark:text-gray-300">{{ t('admin.accounts.codexReset.description', { name: account?.name }) }}</p>
    <p v-if="loading">{{ t('common.loading') }}</p>
    <p v-if="error" role="alert" class="mb-3 text-sm text-red-600">{{ error }}</p>
    <p v-if="result" role="status" class="mb-3 text-sm">{{ t(`admin.accounts.codexReset.${result.outcome}`) }}</p>
    <p v-if="result && result.outcome !== 'no_credit' && !result.quota_synced" class="mb-3 text-sm text-amber-600">{{ t('admin.accounts.codexReset.syncFailed') }}</p>
    <button v-if="pending" class="btn btn-primary mb-4" :disabled="busy" @click="consume(pending.creditId)">{{ t('admin.accounts.codexReset.retry') }}</button>
    <template v-if="!loading && loaded">
      <p class="mb-3 text-sm font-medium">{{ t('admin.accounts.codexReset.available', { count: availableCount }) }}</p>
      <p v-if="!credits.length" class="text-sm text-gray-500">{{ t('admin.accounts.codexReset.empty') }}</p>
      <div v-for="credit in credits" :key="credit.id" class="mb-3 rounded-xl border border-gray-200 p-3 dark:border-dark-700">
        <p class="text-sm font-medium">{{ credit.title || (credit.reset_type === 'codex_rate_limits' ? t('admin.accounts.codexReset.fullReset') : credit.reset_type) }}</p>
        <p class="text-xs text-gray-500">{{ t(`admin.accounts.codexReset.${['available', 'redeeming', 'redeemed'].includes(credit.status) ? credit.status : 'unknown'}Status`) }}</p>
        <p v-if="credit.expires_at" class="mt-1 text-xs text-gray-500">{{ t('admin.accounts.codexReset.expires', { date: new Date(credit.expires_at).toLocaleString() }) }}</p>
        <button v-if="usable(credit)" class="btn btn-primary mt-2" :disabled="busy || !!pending" @click="consume(credit.id)">{{ t('admin.accounts.codexReset.use') }}</button>
      </div>
    </template>
    <template #footer>
      <button class="btn btn-secondary" :disabled="busy || loading" @click="load()">{{ t('common.refresh') }}</button>
      <button class="btn btn-secondary" :disabled="busy" @click="close">{{ t('common.close') }}</button>
    </template>
  </BaseDialog>
</template>

<script setup lang="ts">
import { ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import BaseDialog from '@/components/common/BaseDialog.vue'
import { accountsAPI, type CodexResetCredit, type CodexResetResult } from '@/api/admin/accounts'
import type { Account } from '@/types'

const props = defineProps<{ show: boolean; account: Account | null }>()
const emit = defineEmits<{ close: []; updated: [] }>()
const { t } = useI18n()
const loading = ref(false)
const loaded = ref(false)
const busy = ref(false)
const credits = ref<CodexResetCredit[]>([])
const availableCount = ref(0)
const error = ref('')
const result = ref<CodexResetResult | null>(null)
const pending = ref<{ creditId: string; key: string } | null>(null)
const storageKey = () => `codex-reset-pending:${props.account?.id}`
const usable = (credit: CodexResetCredit) => !!credit.id && credit.reset_type === 'codex_rate_limits' && credit.status === 'available' &&
  (!credit.expires_at || new Date(credit.expires_at).getTime() > Date.now())
const close = () => { if (!busy.value) emit('close') }
const message = (value: unknown) => value instanceof Error ? value.message :
  (value as { message?: string } | null)?.message || t('admin.accounts.codexReset.failed')

let loadSequence = 0
async function load(clearError = true) {
  const id = props.account?.id
  if (!id || busy.value) return
  const sequence = ++loadSequence
  loading.value = true
  if (clearError) error.value = ''
  try {
    const details = await accountsAPI.listCodexResetCredits(id)
    if (id !== props.account?.id || sequence !== loadSequence) return
    credits.value = details.credits
    availableCount.value = details.available_count
    loaded.value = true
  } catch (value) { if (id === props.account?.id && sequence === loadSequence) error.value = message(value) }
  finally { if (sequence === loadSequence) loading.value = false }
}

async function consume(creditId: string) {
  const id = props.account?.id
  if (!id || busy.value) return
  busy.value = true
  error.value = ''
  result.value = null
  try {
    if (!pending.value) {
      // Persist before sending so a reopened dialog/page can safely retry.
      const bytes = crypto.getRandomValues(new Uint8Array(16))
      const key = Array.from(bytes, byte => byte.toString(16).padStart(2, '0')).join('')
      const request = { creditId, key }
      sessionStorage.setItem(storageKey(), JSON.stringify(request))
      pending.value = request
    }
    const response = await accountsAPI.consumeCodexResetCredit(id, pending.value.creditId, pending.value.key)
    result.value = response
    if (response.quota_synced || response.outcome === 'no_credit') {
      sessionStorage.removeItem(storageKey())
      pending.value = null
    }
    emit('updated')
  } catch (value) { error.value = message(value) }
  finally { busy.value = false }
  await load(false)
}

watch(() => [props.show, props.account?.id] as const, ([show]) => {
  if (!show) return
  credits.value = []
  availableCount.value = 0
  loaded.value = false
  result.value = null
  error.value = ''
  pending.value = null
  try {
    const stored = JSON.parse(sessionStorage.getItem(storageKey()) || 'null')
    if (stored && typeof stored.creditId === 'string' && typeof stored.key === 'string') pending.value = stored
  } catch { /* Storage failure prevents sending a new consumable request. */ }
  void load()
}, { immediate: true })
</script>
