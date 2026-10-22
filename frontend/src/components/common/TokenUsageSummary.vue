<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import TokenCount from './TokenCount.vue'
import { cacheReadRate, formatCacheReadRate, formatTokenCount, tokenBreakdown, type TokenPeriod, type TokenUsageSource } from '@/utils/usageTokens'

const props = withDefaults(defineProps<{
  data?: TokenUsageSource | null
  period?: TokenPeriod
  compact?: boolean
  /** Stat-card density: smaller total and the breakdown in one wrapping line. */
  dense?: boolean
  showBreakdown?: boolean
}>(), { period: '', compact: false, dense: false, showBreakdown: true })
const { t } = useI18n()
const counts = computed(() => tokenBreakdown(props.data, props.period))
</script>

<template>
  <div class="min-w-0" :class="dense ? 'space-y-0.5' : 'space-y-1'" data-testid="token-usage-summary">
    <div :class="compact ? 'font-medium' : dense ? 'text-lg font-bold leading-tight' : 'text-xl font-bold'" class="text-gray-900 dark:text-white">
      <TokenCount :value="counts.total" :breakdown="counts" />
    </div>
    <p v-if="showBreakdown && dense" class="flex flex-wrap gap-x-2 text-xs tabular-nums">
      <span class="text-emerald-600 dark:text-emerald-400">{{ t('tokens.input') }}: {{ formatTokenCount(counts.input) }}</span>
      <span class="text-violet-600 dark:text-violet-400">{{ t('tokens.output') }}: {{ formatTokenCount(counts.output) }}</span>
      <span class="text-sky-600 dark:text-sky-400">{{ t('tokens.cacheRead') }}: {{ formatTokenCount(counts.cacheRead) }}</span>
      <span class="text-amber-600 dark:text-amber-400">{{ t('tokens.cacheWrite') }}: {{ formatTokenCount(counts.cacheWrite) }}</span>
      <span class="text-gray-600 dark:text-gray-400" :title="t('tokens.cacheReadRateHelp')" data-testid="cache-read-rate">
        {{ t('tokens.cacheReadRate') }}: {{ formatCacheReadRate(cacheReadRate(data, period)) }}
      </span>
    </p>
    <template v-else-if="showBreakdown">
      <p class="flex flex-wrap gap-x-2 text-xs tabular-nums">
        <span class="text-emerald-600 dark:text-emerald-400">{{ t('tokens.input') }}: {{ formatTokenCount(counts.input) }}</span>
        <span class="text-violet-600 dark:text-violet-400">{{ t('tokens.output') }}: {{ formatTokenCount(counts.output) }}</span>
      </p>
      <p class="flex flex-wrap gap-x-2 text-xs tabular-nums">
        <span class="text-sky-600 dark:text-sky-400">{{ t('tokens.cacheRead') }}: {{ formatTokenCount(counts.cacheRead) }}</span>
        <span class="text-amber-600 dark:text-amber-400">{{ t('tokens.cacheWrite') }}: {{ formatTokenCount(counts.cacheWrite) }}</span>
      </p>
      <p class="text-xs tabular-nums text-gray-600 dark:text-gray-400" :title="t('tokens.cacheReadRateHelp')" data-testid="cache-read-rate">
        {{ t('tokens.cacheReadRate') }}: {{ formatCacheReadRate(cacheReadRate(data, period)) }}
      </p>
    </template>
  </div>
</template>
