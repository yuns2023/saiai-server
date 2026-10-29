<template>
  <div class="min-w-0" role="img" :aria-label="ariaLabel" data-testid="token-mix-bar">
    <HelpTooltip lazy class="!ml-0 w-full">
      <template #trigger>
        <!-- Hover-only detail; the root role="img" label carries the percentages and exact counts. -->
        <span
          class="flex h-2 w-full min-w-[5rem] cursor-help overflow-hidden rounded-full bg-gray-100 dark:bg-dark-700"
          aria-hidden="true"
        >
          <span
            v-for="segment in segments"
            :key="segment.key"
            class="h-full"
            :class="segment.barClass"
            :style="{ width: shareBarWidth(segment.share) }"
          ></span>
        </span>
      </template>
      <dl class="grid grid-cols-[auto_auto_auto] gap-x-4 gap-y-1 tabular-nums">
        <template v-for="segment in segments" :key="segment.key">
          <dt :class="segment.tooltipClass">{{ segment.label }}</dt>
          <dd class="text-right">{{ formatAnalyticsShare(segment.share) }}</dd>
          <dd class="text-right text-gray-300">{{ formatExactTokens(segment.count) }}</dd>
        </template>
      </dl>
      <p class="mt-2 border-t border-white/20 pt-2 text-gray-300">
        {{ t('tokens.total') }}: {{ formatExactTokens(counts.total) }}
      </p>
    </HelpTooltip>
    <ul v-if="legend" class="mt-2 flex flex-wrap gap-x-4 gap-y-1 text-xs tabular-nums">
      <li v-for="segment in segments" :key="segment.key" class="inline-flex items-center gap-1.5">
        <span class="h-2 w-2 rounded-full" :class="segment.barClass"></span>
        <span class="text-gray-600 dark:text-gray-400">{{ segment.label }}</span>
        <span class="font-medium text-gray-900 dark:text-white">{{ formatAnalyticsShare(segment.share) }}</span>
      </li>
    </ul>
  </div>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import HelpTooltip from '@/components/common/HelpTooltip.vue'
import type { UsageAnalyticsMetrics, UsageAnalyticsTokenMix } from '@/api/usage'
import { formatExactTokens, tokenBreakdown } from '@/utils/usageTokens'
import { formatAnalyticsShare, shareBarWidth } from '@/utils/usageAnalytics'

const props = withDefaults(
  defineProps<{
    mix: UsageAnalyticsTokenMix
    data: UsageAnalyticsMetrics
    legend?: boolean
  }>(),
  { legend: false }
)

const { t } = useI18n()
const counts = computed(() => tokenBreakdown(props.data))
// Colors match TokenUsageSummary / TokenCount.
const segments = computed(() => [
  {
    key: 'input',
    label: t('tokens.input'),
    share: props.mix.input,
    count: counts.value.input,
    barClass: 'bg-emerald-500',
    tooltipClass: 'text-emerald-300'
  },
  {
    key: 'output',
    label: t('tokens.output'),
    share: props.mix.output,
    count: counts.value.output,
    barClass: 'bg-violet-500',
    tooltipClass: 'text-violet-300'
  },
  {
    key: 'cache_read',
    label: t('tokens.cacheRead'),
    share: props.mix.cache_read,
    count: counts.value.cacheRead,
    barClass: 'bg-sky-500',
    tooltipClass: 'text-sky-300'
  },
  {
    key: 'cache_creation',
    label: t('tokens.cacheWrite'),
    share: props.mix.cache_creation,
    count: counts.value.cacheWrite,
    barClass: 'bg-amber-500',
    tooltipClass: 'text-amber-300'
  }
])
const ariaLabel = computed(() =>
  segments.value
    .map((segment) => `${segment.label} ${formatAnalyticsShare(segment.share)} (${formatExactTokens(segment.count)})`)
    .join(', ')
)
</script>
