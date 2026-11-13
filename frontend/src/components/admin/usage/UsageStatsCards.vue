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
      </div>
    </div>
    <div class="card p-4 flex items-center gap-3">
      <div class="rounded-lg bg-green-100 p-2 dark:bg-green-900/30 text-green-600">
        <Icon name="dollar" size="md" />
      </div>
      <div class="min-w-0 flex-1">
        <UsageBillingSummary :stats="stats">
          <template v-if="stats?.total_account_cost != null" #extra>
            <span :title="t('usage.accountBilledHint')" data-testid="usage-stats-account-cost">
              {{ t('usage.accountBilled') }}
              <span class="font-medium text-gray-700 dark:text-gray-300">${{ stats.total_account_cost.toFixed(4) }}</span>
            </span>
            <span v-if="stats.total_metered_cost != null" :title="t('admin.accounts.stats.hostedReferenceHint')" data-testid="usage-stats-hosted-cost">
              {{ t('admin.accounts.stats.hostedReference') }}
              <span class="font-medium text-gray-700 dark:text-gray-300">≈${{ stats.total_metered_cost.toFixed(4) }}</span>
            </span>
          </template>
        </UsageBillingSummary>
        <p
          v-if="stats?.total_subscription_revenue != null"
          class="mt-0.5 text-xs text-gray-500 dark:text-gray-400"
          data-testid="usage-stats-revenue"
        >
          {{ t('usage.revenue') }}
          <RevenueAmount
            class="font-semibold text-emerald-700 dark:text-emerald-400"
            :parts="revenueParts"
            :billed-cost="stats.total_actual_cost"
          />
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
import UsageBillingSummary from '@/components/common/UsageBillingSummary.vue'
import TokenUsageSummary from '@/components/common/TokenUsageSummary.vue'
import RevenueAmount from '@/components/common/RevenueAmount.vue'
import { computed } from 'vue'
import { revenuePartsFromTotals } from '@/utils/revenue'
import { useI18n } from 'vue-i18n'
import type { AdminUsageStatsResponse } from '@/api/admin/usage'
import Icon from '@/components/icons/Icon.vue'

const props = defineProps<{ stats: AdminUsageStatsResponse | null }>()

// The summary reports unpriced consumption without the total_ prefix.
const revenueParts = computed(() =>
  revenuePartsFromTotals({
    ...(props.stats ?? {}),
    total_subscription_revenue_unpriced_cost: props.stats?.subscription_revenue_unpriced_cost,
  }),
)

const { t } = useI18n()

const formatDuration = (ms: number) =>
  ms < 1000 ? `${ms.toFixed(0)}ms` : `${(ms / 1000).toFixed(2)}s`

</script>
