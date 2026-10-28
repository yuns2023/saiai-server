<template>
  <div class="flex items-center gap-0.5" data-testid="usage-cost-legend">
    <span>{{ label }}</span>
    <HelpTooltip>
      <template #trigger>
        <button
          type="button"
          class="inline-flex h-4 w-4 cursor-help items-center justify-center rounded-full normal-case text-gray-400 hover:text-primary-600 focus:outline-hidden focus-visible:ring-2 focus-visible:ring-primary-500/50 dark:text-gray-500 dark:hover:text-primary-400"
          :aria-label="t('usage.costLegendTitle')"
          data-testid="usage-cost-legend-trigger"
        >
          <Icon name="infoCircle" size="xs" />
        </button>
      </template>
      <div class="space-y-1.5 normal-case tracking-normal">
        <p class="font-semibold">{{ t('usage.costLegendTitle') }}</p>
        <p v-for="item in items" :key="item.key" class="flex items-center gap-2">
          <span :class="['inline-flex h-5 w-5 shrink-0 items-center justify-center rounded-md', item.class]">
            <Icon :name="item.icon" size="xs" />
          </span>
          <span>{{ item.label }}</span>
        </p>
        <p class="text-gray-300">{{ t('usage.costLegendStandardHidden') }}</p>
        <p v-if="showAccountCost" class="text-gray-300">A $…: {{ t('usage.accountBilled') }}</p>
        <p v-if="showReference" class="text-gray-300">≈$…: {{ t('hostedUsage.reference') }}</p>
      </div>
    </HelpTooltip>
  </div>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import HelpTooltip from './HelpTooltip.vue'
import Icon from '@/components/icons/Icon.vue'
import { usageBillingLabel } from '@/utils/usageBilling'
import { usageBillingVisual, usageServiceTierVisual } from '@/utils/usageCostVisual'

defineProps<{
  label: string
  /** Admin tables also show the platform account cost ("A $…"). */
  showAccountCost?: boolean
  /** Show the hosted-usage reference amount ("≈$…"); hosted rows bill $0. */
  showReference?: boolean
}>()

const { t } = useI18n()

const items = computed(() => {
  const priority = usageServiceTierVisual('priority')!
  const flex = usageServiceTierVisual('flex')!
  return [
    { key: 'balance', label: t(usageBillingLabel(0)), ...usageBillingVisual(0) },
    { key: 'subscription', label: t(usageBillingLabel(1)), ...usageBillingVisual(1) },
    { key: 'unclassified', label: t(usageBillingLabel(undefined)), ...usageBillingVisual(undefined) },
    { key: 'priority', label: `${t('usage.serviceTier')}: ${t('usage.serviceTierPriority')}`, ...priority },
    { key: 'flex', label: `${t('usage.serviceTier')}: ${t('usage.serviceTierFlex')}`, ...flex },
  ]
})
</script>
