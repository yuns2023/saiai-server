<template>
  <div class="min-w-0 space-y-0.5" data-testid="usage-billing-summary">
    <div class="flex items-center text-xs font-medium text-gray-500 dark:text-gray-400">
      <span>{{ t('usage.balanceCharge') }}</span>
      <HelpTooltip>
        <template #trigger>
          <button
            type="button"
            class="inline-flex h-4 w-4 cursor-help items-center justify-center rounded-full text-gray-400 transition-colors hover:text-primary-600 focus:outline-hidden focus-visible:ring-2 focus-visible:ring-primary-500/50 dark:text-gray-500 dark:hover:text-primary-400"
            :aria-label="hintLabel"
            data-testid="usage-billing-hint"
          >
            <Icon name="infoCircle" size="xs" />
          </button>
        </template>
        <p>
          <span class="font-semibold">{{ t('usage.subscriptionUsage') }}</span>:
          {{ t('usage.subscriptionUsageHint') }}
        </p>
        <p v-if="baseCost !== undefined" class="mt-1.5">
          <span class="font-semibold">{{ t('usage.standardCost') }}</span>:
          {{ t('usage.baseCostHint') }}
        </p>
      </HelpTooltip>
    </div>
    <p class="text-lg font-bold leading-tight tabular-nums text-green-600 dark:text-green-400">
      {{ amount(stats?.total_balance_cost) }}
    </p>
    <p class="flex flex-wrap gap-x-3 text-xs tabular-nums text-gray-500 dark:text-gray-400">
      <span :title="t('usage.subscriptionUsageHint')">
        {{ t('usage.subscriptionUsage') }}
        <span class="font-semibold text-gray-800 dark:text-gray-200">{{ amount(stats?.total_subscription_cost) }}</span>
      </span>
      <span v-if="baseCost !== undefined" :title="t('usage.baseCostHint')">
        {{ t('usage.standardCost') }}
        <span class="font-medium text-gray-700 dark:text-gray-300">{{ amount(baseCost) }}</span>
      </span>
      <slot name="extra" />
    </p>
    <p v-if="stats?.total_unclassified_cost" class="text-xs text-amber-600">{{ t('usage.unclassifiedUsage') }}: {{ amount(stats.total_unclassified_cost) }}</p>
  </div>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import HelpTooltip from './HelpTooltip.vue'
import Icon from '@/components/icons/Icon.vue'

const props = withDefaults(defineProps<{
  stats: { total_balance_cost?: number; total_subscription_cost?: number; total_unclassified_cost?: number } | null
  /** Configured base-price total; rendered (with its hint) when provided. null = unavailable. */
  baseCost?: number | null
}>(), { baseCost: undefined })
const { t } = useI18n()
const amount = (value: number | undefined | null) => value == null ? '—' : `$${value.toFixed(4)}`
// Explanatory hints live in the info tooltip; the accessible name carries the same text.
const hintLabel = computed(() => {
  const hints = [`${t('usage.subscriptionUsage')}: ${t('usage.subscriptionUsageHint')}`]
  if (props.baseCost !== undefined) hints.push(`${t('usage.standardCost')}: ${t('usage.baseCostHint')}`)
  return hints.join(' ')
})
</script>
