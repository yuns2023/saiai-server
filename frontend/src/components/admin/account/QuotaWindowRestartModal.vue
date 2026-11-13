<template>
  <BaseDialog :show="show" :title="t('admin.accounts.quotaWindowRestart.title')" :close-on-escape="!busy" @close="close">
    <p class="mb-4 text-sm text-gray-600 dark:text-gray-300">{{ t('admin.accounts.quotaWindowRestart.description') }}</p>
    <p v-if="loading" class="text-sm text-gray-500">{{ t('admin.accounts.quotaWindowRestart.loading') }}</p>
    <p v-if="error" role="alert" class="mb-3 text-sm text-red-600" data-testid="restart-error">{{ error }}</p>
    <template v-if="!loading && loaded">
      <p v-if="!windowInfo" class="text-sm text-amber-600" data-testid="restart-no-window">{{ t('admin.accounts.quotaWindowRestart.noWindow') }}</p>
      <template v-else>
        <p class="mb-2 text-sm" data-testid="restart-window">
          {{ t('admin.accounts.quotaWindowRestart.currentReset', { start: formatTime(windowInfo.start), reset: formatTime(windowInfo.reset) }) }}
        </p>
        <p v-if="current" class="mb-3 text-sm text-emerald-700 dark:text-emerald-400" data-testid="restart-current">
          {{ t('admin.accounts.quotaWindowRestart.current', { time: formatTime(new Date(current.start)), source: sourceText(current.source) }) }}
        </p>
        <label class="input-label" for="quota-window-restart-start">{{ t('admin.accounts.quotaWindowRestart.startLabel') }}</label>
        <input
          id="quota-window-restart-start"
          v-model="startLocal"
          type="datetime-local"
          step="1"
          class="input"
          :min="toLocalInput(earliestStart(windowInfo.start))"
          :max="toLocalInput(new Date())"
          data-testid="restart-start"
        />
        <p class="input-hint">{{ t('admin.accounts.quotaWindowRestart.startHint', { min: formatTime(earliestStart(windowInfo.start)) }) }}</p>
      </template>
    </template>
    <template #footer>
      <button class="btn btn-secondary" :disabled="busy" @click="close">{{ t('common.cancel') }}</button>
      <button v-if="current && windowInfo" class="btn btn-secondary" :disabled="busy || loading" data-testid="restart-clear" @click="clear">
        {{ t('admin.accounts.quotaWindowRestart.clear') }}
      </button>
      <button class="btn btn-primary" :disabled="busy || loading || !windowInfo" data-testid="restart-confirm" @click="submit">
        {{ t('admin.accounts.quotaWindowRestart.confirm') }}
      </button>
    </template>
  </BaseDialog>
</template>

<script setup lang="ts">
import { ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import BaseDialog from '@/components/common/BaseDialog.vue'
import { accountsAPI } from '@/api/admin/accounts'
import { useAppStore } from '@/stores/app'
import type { Account, UsageWindowRestart } from '@/types'

const SEVEN_DAYS_MS = 7 * 24 * 60 * 60 * 1000

const props = defineProps<{ show: boolean; account: Account | null }>()
const emit = defineEmits<{ close: []; updated: [] }>()
const { t } = useI18n()
const appStore = useAppStore()

const loading = ref(false)
const loaded = ref(false)
const busy = ref(false)
const error = ref('')
const windowInfo = ref<{ start: Date; reset: Date } | null>(null)
const current = ref<UsageWindowRestart | null>(null)
const startLocal = ref('')
// The default "now" is sent as no start (server time): a datetime-local value
// has no UTC offset and is ambiguous in a DST fall-back hour.
const initialStartLocal = ref('')

const pad = (n: number) => String(n).padStart(2, '0')
// datetime-local works in the browser's local time without a zone suffix.
const toLocalInput = (d: Date) =>
  `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}T${pad(d.getHours())}:${pad(d.getMinutes())}:${pad(d.getSeconds())}`
const formatTime = (d: Date) => d.toLocaleString()
// The server requires a start strictly after the window start (second precision).
const earliestStart = (windowStart: Date) => new Date(windowStart.getTime() + 1000)
const sourceText = (source: string) =>
  t(source === 'auto' ? 'admin.accounts.quotaWindowRestart.sourceAuto' : 'admin.accounts.quotaWindowRestart.sourceManual')
const message = (value: unknown) => value instanceof Error ? value.message :
  (value as { message?: string } | null)?.message || t('admin.accounts.quotaWindowRestart.failed')
const close = () => { if (!busy.value) emit('close') }

let loadSequence = 0
// keepInput: re-read after a failed action without discarding a time the
// admin picked (a retry must not silently become "now").
async function load(keepInput = false): Promise<string> {
  const account = props.account
  if (!account) return ''
  const sequence = ++loadSequence
  const edited = keepInput && startLocal.value !== initialStartLocal.value
  loading.value = true
  error.value = ''
  try {
    const source = account.platform === 'anthropic' ? 'passive' : undefined
    const usage = await accountsAPI.getUsage(account.id, source)
    if (sequence !== loadSequence || account.id !== props.account?.id) return ''
    const resetsAt = usage.seven_day?.resets_at ? new Date(usage.seven_day.resets_at) : null
    const valid = resetsAt && !Number.isNaN(resetsAt.getTime()) && resetsAt.getTime() > Date.now() &&
      resetsAt.getTime() - Date.now() <= SEVEN_DAYS_MS
    windowInfo.value = valid ? { start: new Date(resetsAt.getTime() - SEVEN_DAYS_MS), reset: resetsAt } : null
    current.value = usage.seven_day?.window_restart ?? null
    if (!edited) {
      startLocal.value = toLocalInput(new Date())
      initialStartLocal.value = startLocal.value
    }
    loaded.value = true
    return ''
  } catch (value) {
    if (sequence !== loadSequence) return ''
    // Never act on a window that could not be re-read.
    windowInfo.value = null
    current.value = null
    loaded.value = true
    error.value = message(value)
    return error.value
  } finally {
    if (sequence === loadSequence) loading.value = false
  }
}

async function submit() {
  const account = props.account
  if (!account || !windowInfo.value || busy.value) return
  // Only an untouched field means "now" (server time); an edited field that
  // is empty or not a valid date must not silently fall back to now.
  const edited = startLocal.value !== initialStartLocal.value
  const start = edited ? new Date(startLocal.value) : null
  if (start && (!startLocal.value || Number.isNaN(start.getTime()))) {
    error.value = t('admin.accounts.quotaWindowRestart.invalidStart')
    return
  }
  busy.value = true
  error.value = ''
  try {
    await accountsAPI.setQuotaWindowRestart(account.id, {
      window: 'seven_day',
      expected_reset: Math.floor(windowInfo.value.reset.getTime() / 1000),
      start: start ? start.toISOString().replace(/\.\d{3}Z$/, 'Z') : undefined
    })
    appStore.showSuccess(t('admin.accounts.quotaWindowRestart.success'))
    emit('updated')
    emit('close')
  } catch (value) {
    await failed(value)
  } finally {
    busy.value = false
  }
}

// Shows the error and re-reads the window, so a 409 (the window moved) can be
// retried against the current reset without reopening the dialog.
async function failed(value: unknown) {
  const text = message(value)
  const reloadError = await load(true)
  error.value = reloadError ? `${text} (${reloadError})` : text
}

async function clear() {
  const account = props.account
  if (!account || busy.value) return
  busy.value = true
  error.value = ''
  try {
    if (!windowInfo.value) return
    await accountsAPI.clearQuotaWindowRestart(account.id, Math.floor(windowInfo.value.reset.getTime() / 1000))
    appStore.showSuccess(t('admin.accounts.quotaWindowRestart.cleared'))
    emit('updated')
    emit('close')
  } catch (value) {
    await failed(value)
  } finally {
    busy.value = false
  }
}

watch(() => [props.show, props.account?.id] as const, ([show]) => {
  if (!show) return
  loaded.value = false
  windowInfo.value = null
  current.value = null
  error.value = ''
  void load()
}, { immediate: true })
</script>
