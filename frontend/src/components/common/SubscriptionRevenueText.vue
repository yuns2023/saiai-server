<template>
  <span class="tabular-nums text-gray-500 dark:text-gray-400" :title="title" data-testid="subscription-revenue">
    <template v-if="!compact">{{ t('usage.subscriptionRevenue') }} </template>
    <span class="font-semibold text-emerald-700 dark:text-emerald-400">≈¥{{ revenue.toFixed(2) }}</span>
    <span v-if="hasUnpriced" class="ml-1 text-amber-600 dark:text-amber-400" data-testid="subscription-revenue-unpriced">
      {{ unpricedText }}
    </span>
  </span>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'

// Statistics-only estimate: subscription consumption × each group's CURRENT
// revenue rate. Consumption in groups without a rate is shown separately so a
// missing rate is never mistaken for a configured free (0) group.
const props = defineProps<{
  revenue: number
  unpricedCost?: number | null
  compact?: boolean
}>()
const { t } = useI18n()
const unpriced = computed(() => props.unpricedCost ?? 0)
const hasUnpriced = computed(() => unpriced.value > 0)
const unpricedLong = computed(() => t('usage.subscriptionRevenueUnpriced', { cost: `$${unpriced.value.toFixed(4)}` }))
const unpricedText = computed(() => (props.compact ? `+$${unpriced.value.toFixed(2)}?` : unpricedLong.value))
const title = computed(() => {
  const hint = t('usage.subscriptionRevenueHint')
  return hasUnpriced.value ? `${hint}\n${unpricedLong.value}` : hint
})
</script>
