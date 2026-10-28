<template>
  <div
    class="grid grid-cols-[auto_auto] items-baseline justify-start gap-x-2 gap-y-0.5 whitespace-nowrap border-l-2 border-gray-200 pl-2 text-xs dark:border-dark-600"
    data-testid="usage-latency-cell"
  >
    <template v-if="row.first_token_ms != null">
      <span class="text-gray-400 dark:text-gray-500">{{ t('usage.firstToken') }}</span>
      <span class="tabular-nums text-gray-700 dark:text-gray-300" data-testid="usage-latency-first-token">{{ formatLatency(row.first_token_ms) }}</span>
    </template>
    <span class="text-gray-400 dark:text-gray-500">{{ t('usage.totalDuration') }}</span>
    <span class="tabular-nums text-gray-700 dark:text-gray-300" data-testid="usage-latency-duration">{{ formatLatency(row.duration_ms) }}</span>
    <template v-if="tps != null">
      <span class="text-gray-400 dark:text-gray-500">{{ t('usage.outputTps') }}</span>
      <HelpTooltip lazy class="!ml-0">
        <template #trigger>
          <button
            type="button"
            class="usage-latency-tps"
            :aria-label="`${t('usage.outputTps')} ${formatTokensPerSecond(tps)}: ${tpsHint}`"
            data-testid="usage-latency-tps"
          >{{ formatTokensPerSecond(tps) }}</button>
        </template>
        {{ tpsHint }}
      </HelpTooltip>
    </template>
  </div>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import HelpTooltip from '@/components/common/HelpTooltip.vue'
import { formatLatency, formatTokensPerSecond, outputTokensPerSecond, type UsageLatencySource } from '@/utils/usageLatency'

const props = defineProps<{ row: UsageLatencySource }>()
const { t } = useI18n()

const tps = computed(() => outputTokensPerSecond(props.row))
const tpsHint = computed(() => t('usage.outputTpsHint', { tokens: props.row.output_tokens ?? 0 }))
</script>

<style scoped>
@reference "../../style.css";

.usage-latency-tps {
  @apply cursor-help rounded-sm text-left tabular-nums text-gray-700 underline decoration-dotted underline-offset-2 dark:text-gray-300;
  @apply focus:outline-hidden focus-visible:ring-2 focus-visible:ring-primary-500/50;
}
</style>
