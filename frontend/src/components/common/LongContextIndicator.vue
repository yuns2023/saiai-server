<template>
  <div v-if="info?.applied" :class="detailed ? 'max-w-sm space-y-1.5 whitespace-normal' : ''" data-testid="long-context-indicator">
    <span class="inline-flex items-center rounded px-1.5 py-0.5 text-[11px] font-semibold leading-tight text-amber-800 bg-amber-100 ring-1 ring-inset ring-amber-300 dark:bg-amber-500/20 dark:text-amber-300 dark:ring-amber-500/40" :title="description">
      {{ t('usage.longContext') }} &gt;{{ thresholdLabel }}<template v-if="info.inferred"> · {{ t('usage.longContextInferred') }}</template>
    </span>
    <template v-if="detailed">
      <p>{{ t('usage.longContextTrigger', { total: info.total_input_tokens.toLocaleString(), threshold: info.threshold.toLocaleString() }) }}</p>
      <p>{{ t(info.mode === 'excess_input' ? 'usage.longContextExcess' : 'usage.longContextWholeRequest') }}</p>
      <p>{{ multipliers }}</p>
      <p v-if="info.inferred" class="text-amber-400">{{ t('usage.longContextInferredHint') }}</p>
    </template>
  </div>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import type { LongContextPricing } from '@/types'

const props = defineProps<{ info?: LongContextPricing | null; detailed?: boolean }>()
const { t } = useI18n()
const thresholdLabel = computed(() => props.info && props.info.threshold % 1000 === 0 ? `${props.info.threshold / 1000}K` : props.info?.threshold.toLocaleString())
const multipliers = computed(() => t('usage.longContextMultipliers', { input: props.info?.input_multiplier, output: props.info?.output_multiplier, read: props.info?.cache_read_multiplier, write: props.info?.cache_write_multiplier }))
const description = computed(() => [t('usage.longContextTrigger', { total: props.info?.total_input_tokens.toLocaleString(), threshold: props.info?.threshold.toLocaleString() }),
  t(props.info?.mode === 'excess_input' ? 'usage.longContextExcess' : 'usage.longContextWholeRequest'), multipliers.value,
  props.info?.inferred ? t('usage.longContextInferredHint') : ''].filter(Boolean).join('\n'))
</script>
