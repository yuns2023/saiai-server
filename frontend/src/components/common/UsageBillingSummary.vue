<template>
  <div class="space-y-1">
    <p class="text-xs text-gray-500">{{ t('usage.balanceCharge') }}</p>
    <p class="text-xl font-bold text-green-600">{{ amount(stats?.total_balance_cost) }}</p>
    <p class="text-xs text-gray-500" :title="t('usage.subscriptionUsageHint')">{{ t('usage.subscriptionUsage') }}</p>
    <p class="text-lg font-semibold">{{ amount(stats?.total_subscription_cost) }}</p>
    <p class="text-xs text-gray-400">{{ t('usage.subscriptionUsageHint') }}</p>
    <p v-if="stats?.total_unclassified_cost" class="text-xs text-amber-600">{{ t('usage.unclassifiedUsage') }}: {{ amount(stats.total_unclassified_cost) }}</p>
  </div>
</template>

<script setup lang="ts">
import { useI18n } from 'vue-i18n'
defineProps<{ stats: { total_balance_cost?: number; total_subscription_cost?: number; total_unclassified_cost?: number } | null }>()
const { t } = useI18n()
const amount = (value: number | undefined) => value == null ? '—' : `$${value.toFixed(4)}`
</script>
