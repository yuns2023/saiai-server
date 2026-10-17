<template>
  <div class="grid grid-cols-2 gap-4 lg:grid-cols-4">
    <div class="card p-4 flex items-center gap-3">
      <div class="rounded-lg bg-blue-100 p-2 dark:bg-blue-900/30 text-blue-600">
        <Icon name="document" size="md" />
      </div>
      <div>
        <p class="text-xs font-medium text-gray-500">{{ t('usage.totalRequests') }}</p>
        <p class="text-xl font-bold">{{ stats?.total_requests?.toLocaleString() || '0' }}</p>
        <p class="text-xs text-gray-400">{{ t('usage.inSelectedRange') }}</p>
      </div>
    </div>
    <div class="card p-4 flex items-center gap-3">
      <div class="rounded-lg bg-amber-100 p-2 dark:bg-amber-900/30 text-amber-600"><svg class="h-5 w-5" fill="none" stroke="currentColor" viewBox="0 0 24 24"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="m21 7.5-9-5.25L3 7.5m18 0-9 5.25m9-5.25v9l-9 5.25M3 7.5l9 5.25M3 7.5v9l9 5.25m0-9v9" /></svg></div>
      <div>
        <p class="text-xs font-medium text-gray-500">{{ t('tokens.total') }}</p>
        <TokenUsageSummary :data="stats" period="total" />
        <p class="text-xs text-gray-400" data-testid="input-cache-read-rate" :title="t('usage.inputCacheReadRateHint')">
          {{ t('usage.inputCacheReadRate') }} {{ inputCacheReadRate === null ? '—' : `${inputCacheReadRate.toFixed(1)}%` }}
        </p>
      </div>
    </div>
    <div class="card p-4 flex items-center gap-3">
      <div class="rounded-lg bg-green-100 p-2 dark:bg-green-900/30 text-green-600">
        <Icon name="dollar" size="md" />
      </div>
      <div class="min-w-0 flex-1">
        <p class="text-xs font-medium text-gray-500">{{ t('usage.userBilled') }}</p>
        <p class="text-xl font-bold text-green-600">
          ${{ (stats?.total_actual_cost || 0).toFixed(4) }}
        </p>
        <p class="text-xs text-gray-400">
          {{ t('usage.standardCost') }}:
          <span>${{ (stats?.total_cost || 0).toFixed(4) }}</span>
          <template v-if="stats?.total_account_cost != null">
            · {{ t('usage.accountBilled') }}:
            <span>${{ (stats?.total_account_cost || 0).toFixed(4) }}</span>
          </template>
        </p>
      </div>
    </div>
    <div class="card p-4 flex items-center gap-3">
      <div class="rounded-lg bg-purple-100 p-2 dark:bg-purple-900/30 text-purple-600">
        <Icon name="clock" size="md" />
      </div>
      <div><p class="text-xs font-medium text-gray-500">{{ t('usage.avgDuration') }}</p><p class="text-xl font-bold">{{ formatDuration(stats?.average_duration_ms || 0) }}</p></div>
    </div>
  </div>
</template>

<script setup lang="ts">
import TokenUsageSummary from '@/components/common/TokenUsageSummary.vue'
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import type { AdminUsageStatsResponse } from '@/api/admin/usage'
import Icon from '@/components/icons/Icon.vue'

const props = defineProps<{ stats: AdminUsageStatsResponse | null }>()

const { t } = useI18n()

const inputCacheReadRate = computed(() => {
  const input = props.stats?.total_input_tokens || 0
  const cacheWrite = props.stats?.total_cache_creation_tokens || 0
  const cacheRead = props.stats?.total_cache_read_tokens || 0
  const totalInput = input + cacheWrite + cacheRead
  if (totalInput <= 0) return null
  return (cacheRead / totalInput) * 100
})

const formatDuration = (ms: number) =>
  ms < 1000 ? `${ms.toFixed(0)}ms` : `${(ms / 1000).toFixed(2)}s`

</script>
