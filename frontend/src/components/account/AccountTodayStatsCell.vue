<template>
  <div>
    <!-- Loading state -->
    <div v-if="props.loading && !props.stats" class="space-y-0.5">
      <div class="h-3 w-12 animate-pulse rounded-sm bg-gray-200 dark:bg-gray-700"></div>
      <div class="h-3 w-16 animate-pulse rounded-sm bg-gray-200 dark:bg-gray-700"></div>
      <div class="h-3 w-10 animate-pulse rounded-sm bg-gray-200 dark:bg-gray-700"></div>
    </div>

    <!-- Error state -->
    <div v-else-if="props.error && !props.stats" class="text-xs text-red-500">
      {{ props.error }}
    </div>

    <!-- Stats data -->
    <div v-else-if="props.stats" class="space-y-0.5 text-xs">
      <!-- Requests -->
      <div class="flex items-center gap-1">
        <span class="text-gray-500 dark:text-gray-400"
          >{{ t('admin.accounts.stats.requests') }}:</span
        >
        <span class="font-medium text-gray-700 dark:text-gray-300">{{
          formatNumber(props.stats.requests)
        }}</span>
      </div>
      <!-- Tokens -->
      <div class="flex items-center gap-1">
        <span class="text-gray-500 dark:text-gray-400"
          >{{ t('admin.accounts.stats.tokens') }}:</span
        >
        <span class="font-medium text-gray-700 dark:text-gray-300"><TokenCount :value="props.stats.tokens" /></span>
      </div>
      <!-- Standard usage: SUM(total_cost) at recorded pricing, before group/user/account multipliers; hosted included -->
      <div v-if="props.stats.standard_cost != null" class="flex items-center gap-1">
        <HelpTooltip lazy class="!ml-0">
          <template #trigger>
            <button
              type="button"
              class="cursor-help rounded-sm text-gray-500 underline decoration-dotted underline-offset-2 focus:outline-hidden focus-visible:ring-2 focus-visible:ring-primary-500/50 dark:text-gray-400"
              :aria-label="`${t('admin.accounts.stats.standardUsage')} ${formatCurrency(props.stats.standard_cost)}: ${t('admin.accounts.stats.standardUsageHint')}`"
              data-testid="today-stats-standard"
            >
              {{ t('admin.accounts.stats.standardUsage') }}:
            </button>
          </template>
          {{ t('admin.accounts.stats.standardUsageHint') }}
        </HelpTooltip>
        <span class="font-medium text-gray-700 dark:text-gray-300">{{
          formatCurrency(props.stats.standard_cost)
        }}</span>
      </div>
      <div
        v-if="(props.stats.hosted_requests ?? 0) > 0"
        class="pl-2 text-[11px] tabular-nums text-gray-500 dark:text-gray-400"
        data-testid="today-stats-hosted"
      >
        <span aria-hidden="true">└ </span>{{ t('admin.accounts.stats.hostedIncluded') }} ≈{{ formatCurrency(props.stats.hosted_cost ?? 0) }}
      </div>
      <!-- Cost (Account) -->
      <div class="flex items-center gap-1">
        <span class="text-gray-500 dark:text-gray-400">{{ t('usage.accountBilled') }}:</span>
        <span class="font-medium text-emerald-600 dark:text-emerald-400">{{
          formatCurrency(props.stats.cost)
        }}</span>
      </div>
      <!-- Cost (User/API Key) -->
      <div v-if="props.stats.user_cost != null" class="flex items-center gap-1">
        <span class="text-gray-500 dark:text-gray-400">{{ t('usage.userBilled') }}:</span>
        <span class="font-medium text-gray-700 dark:text-gray-300">{{
          formatCurrency(props.stats.user_cost)
        }}</span>
      </div>
    </div>

    <!-- No data -->
    <div v-else class="text-xs text-gray-400">-</div>
  </div>
</template>

<script setup lang="ts">
import TokenCount from '@/components/common/TokenCount.vue'
import HelpTooltip from '@/components/common/HelpTooltip.vue'
import { useI18n } from 'vue-i18n'
import type { WindowStats } from '@/types'
import { formatNumber, formatCurrency } from '@/utils/format'

const props = withDefaults(
  defineProps<{
    stats?: WindowStats | null
    loading?: boolean
    error?: string | null
  }>(),
  {
    stats: null,
    loading: false,
    error: null
  }
)

const { t } = useI18n()

// Format large token numbers (e.g., 1234567 -> 1.23M)
</script>
