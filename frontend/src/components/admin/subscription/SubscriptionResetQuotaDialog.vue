<template>
  <BaseDialog
    :show="show"
    :title="t('admin.subscriptions.resetQuotaTitle')"
    width="narrow"
    @close="requestClose"
  >
    <form
      v-if="subscription"
      id="reset-quota-form"
      class="space-y-4"
      @submit.prevent="submit"
    >
      <template v-if="windowOptions.length > 0">
        <p class="text-sm text-gray-600 dark:text-gray-400">
          {{ t('admin.subscriptions.resetQuotaFor', { user: subscription.user?.email }) }}
        </p>
        <div class="space-y-2">
          <label
            v-for="option in windowOptions"
            :key="option.window"
            class="flex cursor-pointer items-center justify-between rounded-lg border border-gray-200 px-3 py-2 dark:border-dark-600"
          >
            <span class="flex items-center gap-2">
              <input
                v-model="form[option.window]"
                type="checkbox"
                :data-window="option.window"
                class="h-4 w-4 rounded-sm border-gray-300 text-primary-600 focus:ring-primary-500"
              />
              <span class="text-sm font-medium text-gray-900 dark:text-white">{{ option.label }}</span>
              <span v-if="option.full" class="badge badge-danger">{{ t('admin.subscriptions.resetWindowFull') }}</span>
            </span>
            <span class="text-xs text-gray-500 dark:text-gray-400">
              {{ t('admin.subscriptions.resetWindowUsage', { used: option.used.toFixed(2), limit: option.limit.toFixed(2) }) }}
            </span>
          </label>
        </div>
        <p
          v-if="form.monthly"
          data-testid="monthly-warning"
          class="rounded-lg bg-amber-50 p-3 text-sm text-amber-700 dark:bg-amber-900/20 dark:text-amber-300"
        >
          {{ t('admin.subscriptions.resetMonthlyWarning') }}
        </p>
        <p class="input-hint">{{ t('admin.subscriptions.resetRestartHint') }}</p>
        <div>
          <label class="input-label">{{ t('admin.subscriptions.notesLabel') }}</label>
          <textarea
            v-model="form.notes"
            rows="2"
            class="input"
            :maxlength="ADMIN_NOTES_MAX_LENGTH"
            :placeholder="t('admin.subscriptions.notesPlaceholder')"
          ></textarea>
          <p class="input-hint">{{ t('admin.subscriptions.notesVisibleHint') }}</p>
        </div>
      </template>
      <p v-else class="text-sm text-gray-600 dark:text-gray-400">
        {{ t('admin.subscriptions.resetNoLimits') }}
      </p>
    </form>
    <template #footer>
      <div class="flex justify-end gap-3">
        <button type="button" class="btn btn-secondary" :disabled="submitting" @click="requestClose">
          {{ t('common.cancel') }}
        </button>
        <button
          v-if="windowOptions.length > 0"
          type="submit"
          form="reset-quota-form"
          class="btn btn-primary"
          :disabled="submitting || !hasSelection"
        >
          {{ t('admin.subscriptions.resetQuota') }}
        </button>
      </div>
    </template>
  </BaseDialog>
</template>

<script setup lang="ts">
import { computed, reactive, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { adminAPI } from '@/api/admin'
import { useAppStore } from '@/stores/app'
import type { UserSubscription } from '@/types'
import BaseDialog from '@/components/common/BaseDialog.vue'
import { ADMIN_NOTES_MAX_LENGTH, type SubscriptionWindow } from '@/utils/adminSubscriptionRecords'

const props = defineProps<{
  show: boolean
  subscription: UserSubscription | null
}>()

const emit = defineEmits<{
  (e: 'close'): void
  (e: 'reset'): void
}>()

const { t } = useI18n()
const appStore = useAppStore()

const WINDOWS: SubscriptionWindow[] = ['five_hour', 'daily', 'weekly', 'monthly']

const form = reactive<Record<SubscriptionWindow, boolean> & { notes: string }>({
  five_hour: false,
  daily: false,
  weekly: false,
  monthly: false,
  notes: ''
})
const submitting = ref(false)

// Windows the subscription's group actually limits, with current usage.
const windowOptions = computed(() => {
  const sub = props.subscription
  if (!sub) return []
  const windows: Array<{ window: SubscriptionWindow; label: string; used?: number | null; limit?: number | null }> = [
    { window: 'five_hour', label: t('admin.subscriptions.fiveHour'), used: sub.five_hour_usage_usd, limit: sub.group?.five_hour_limit_usd },
    { window: 'daily', label: t('admin.subscriptions.daily'), used: sub.daily_usage_usd, limit: sub.group?.daily_limit_usd },
    { window: 'weekly', label: t('admin.subscriptions.weekly'), used: sub.weekly_usage_usd, limit: sub.group?.weekly_limit_usd },
    { window: 'monthly', label: t('admin.subscriptions.monthly'), used: sub.monthly_usage_usd, limit: sub.group?.monthly_limit_usd }
  ]
  return windows
    .filter((w) => (w.limit ?? 0) > 0)
    .map((w) => {
      const used = w.used ?? 0
      const limit = w.limit as number
      return { window: w.window, label: w.label, used, limit, full: used >= limit }
    })
})

// While the request is in flight the dialog stays open, so its result
// cannot land on a dialog opened later for another subscription.
const requestClose = () => {
  if (!submitting.value) emit('close')
}

const hasSelection = computed(() => windowOptions.value.some((option) => form[option.window]))

// On open: preselect the windows that are used up (they are what blocks the
// user), but never the monthly one, which is worth a whole month of usage.
watch(
  () => [props.show, props.subscription?.id] as const,
  ([show]) => {
    if (!show) return
    form.notes = ''
    for (const window of WINDOWS) form[window] = false
    for (const option of windowOptions.value) {
      form[option.window] = option.full && option.window !== 'monthly'
    }
  },
  { immediate: true }
)

const submit = async () => {
  const sub = props.subscription
  if (!sub || submitting.value) return
  if (!hasSelection.value) {
    appStore.showError(t('admin.subscriptions.resetSelectAtLeastOne'))
    return
  }
  // Only windows listed above (limited in this group) can be reset.
  const listed = new Set(windowOptions.value.map((option) => option.window))
  const pick = (window: SubscriptionWindow) => listed.has(window) && form[window]
  submitting.value = true
  try {
    await adminAPI.subscriptions.resetQuota(sub.id, {
      five_hour: pick('five_hour'),
      daily: pick('daily'),
      weekly: pick('weekly'),
      monthly: pick('monthly'),
      notes: form.notes.trim() || undefined
    })
    appStore.showSuccess(t('admin.subscriptions.quotaResetSuccess'))
    emit('reset')
  } catch (error: any) {
    appStore.showError(error?.message || error?.response?.data?.detail || t('admin.subscriptions.failedToResetQuota'))
    console.error('Error resetting quota:', error)
  } finally {
    submitting.value = false
  }
}
</script>
