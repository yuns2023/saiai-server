<script setup lang="ts">
import { ref, watch, onUnmounted } from 'vue'
import { useI18n } from 'vue-i18n'
import { apiClient } from '@/api/client'
import type { AdminUsageLog, PaginatedResponse } from '@/types'
import ModerationResultCell from './ModerationResultCell.vue'
import { formatDateTime } from '@/utils/format'

const props = defineProps<{ userId?: number; startTime: string; endTime: string; refreshKey: number }>()
const { t } = useI18n()
type Alert = Pick<AdminUsageLog, 'moderation_result' | 'moderation_events'> & {
  request_id: string; source: string; turn_number: number; user_id: number
}
const rows = ref<Alert[]>([])
const page = ref(1)
const total = ref(0)
const loading = ref(false)
const failed = ref(false)
let controller: AbortController | undefined
async function load() {
  controller?.abort()
  const current = new AbortController()
  controller = current
  loading.value = true
  failed.value = false
  try {
    const { data } = await apiClient.get<PaginatedResponse<Alert>>('/admin/usage/input-risk-alerts', {
      params: { user_id: props.userId, start_time: props.startTime, end_time: props.endTime, page: page.value, page_size: 20 },
      signal: current.signal
    })
    if (current.signal.aborted) return
    rows.value = data.items
    total.value = data.total
  } catch {
    if (!current.signal.aborted) { failed.value = true; rows.value = [] }
  } finally {
    if (!current.signal.aborted) loading.value = false
  }
}
watch(() => [props.userId, props.startTime, props.endTime, props.refreshKey], () => { page.value = 1; void load() }, { immediate: true })
function changePage(delta: number) { page.value += delta; void load() }
onUnmounted(() => controller?.abort())
</script>

<template>
  <section class="card space-y-3 p-4" :aria-busy="loading">
    <h3 class="font-semibold">{{ t('admin.groups.inputModeration.alertHistory') }} <span v-if="!failed && !loading">({{ total }})</span></h3>
    <p class="text-xs text-gray-500">{{ t('admin.groups.inputModeration.alertScope') }}</p>
    <p v-if="failed" role="alert">{{ t('admin.groups.inputModeration.loadAlertsFailed') }}</p>
    <p v-else-if="loading">{{ t('common.loading') }}</p>
    <p v-else-if="!rows.length">{{ t('admin.groups.inputModeration.noAlerts') }}</p>
    <div v-else class="overflow-x-auto">
      <table class="w-full text-left text-sm">
        <thead><tr><th>{{ t('admin.groups.inputModeration.detectionTime') }}</th><th>{{ t('admin.groups.inputModeration.alertIdentity') }}</th><th>{{ t('admin.groups.inputModeration.usageColumn') }}</th></tr></thead>
        <tbody><tr v-for="(row, index) in rows" :key="index">
          <td class="py-2">{{ formatDateTime(row.moderation_result!.updated_at) }}</td>
          <td class="max-w-xs break-all">#{{ row.user_id }} · {{ row.request_id }} · {{ row.turn_number }}</td>
          <td><ModerationResultCell :row="row" /></td>
        </tr></tbody>
      </table>
    </div>
    <div class="flex items-center gap-3">
      <button class="btn btn-secondary" :disabled="loading || page <= 1" @click="changePage(-1)">{{ t('pagination.previous') }}</button>
      <span>{{ page }}</span>
      <button class="btn btn-secondary" :disabled="loading || failed || page * 20 >= total" @click="changePage(1)">{{ t('pagination.next') }}</button>
    </div>
  </section>
</template>
