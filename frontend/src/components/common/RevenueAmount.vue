<template>
  <span class="tabular-nums" :title="title" data-testid="revenue-amount">
    <template v-if="label">{{ label }}</template>{{ amount.toFixed(2) }}<span
      v-if="unpriced > 0"
      class="ml-1 text-amber-600 dark:text-amber-400"
      data-testid="revenue-amount-unpriced"
    >+${{ unpriced.toFixed(2) }}?</span>
  </span>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import type { RevenueParts } from '@/types'

// Admin-only revenue estimate: balance charges ($, taken at face value) plus
// subscription consumption × each group's CURRENT revenue rate. The two parts
// are added without a currency, as the operator asked. Subscription usage in
// groups without a rate and charges with an unknown billing type are not
// included; the title lists every part so the sum can be checked.
const props = defineProps<{
  parts: RevenueParts
  // Total billed actual_cost (balance + subscription quota), shown in the title.
  billedCost?: number | null
  label?: string
}>()
const { t } = useI18n()

const num = (v: number | null | undefined) => (typeof v === 'number' && Number.isFinite(v) ? v : 0)
const balance = computed(() => num(props.parts.balance_cost))
const revenue = computed(() => num(props.parts.subscription_revenue))
const unpriced = computed(() => num(props.parts.subscription_revenue_unpriced_cost))
const unclassified = computed(() => num(props.parts.unclassified_cost))
const amount = computed(() => balance.value + revenue.value)

const title = computed(() => {
  const lines = [
    t('usage.revenueHint'),
    t('usage.revenueBalance', { cost: `$${balance.value.toFixed(4)}` }),
    t('usage.revenueSubscription', {
      cost: `$${num(props.parts.subscription_cost).toFixed(4)}`,
      revenue: revenue.value.toFixed(4),
    }),
  ]
  if (unpriced.value > 0) {
    lines.push(t('usage.subscriptionRevenueUnpriced', { cost: `$${unpriced.value.toFixed(4)}` }))
  }
  if (unclassified.value > 0) {
    lines.push(t('usage.revenueUnclassified', { cost: `$${unclassified.value.toFixed(4)}` }))
  }
  if (props.billedCost != null) {
    lines.push(t('usage.revenueBilled', { cost: `$${num(props.billedCost).toFixed(4)}` }))
  }
  return lines.join('\n')
})
</script>
