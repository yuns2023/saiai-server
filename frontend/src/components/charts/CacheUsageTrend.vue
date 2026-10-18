<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import { Chart as ChartJS, CategoryScale, LinearScale, PointElement, LineElement, Tooltip, Legend } from 'chart.js'
import { Line } from 'vue-chartjs'
import LoadingSpinner from '@/components/common/LoadingSpinner.vue'
import { cacheReadRate, formatCacheReadRate, formatTokenCount } from '@/utils/usageTokens'
import type { TrendDataPoint } from '@/types'

ChartJS.register(CategoryScale, LinearScale, PointElement, LineElement, Tooltip, Legend)
const props = defineProps<{ trendData: TrendDataPoint[], loading?: boolean }>()
const { t } = useI18n()
const rate = computed(() => {
  if (!props.trendData.length || props.trendData.some(point => cacheReadRate(point) == null &&
    (point.input_tokens !== 0 || point.cache_read_tokens !== 0 || point.cache_creation_tokens !== 0))) return null
  return cacheReadRate(props.trendData.reduce((sum, point) => ({
    input_tokens: sum.input_tokens + point.input_tokens,
    cache_read_tokens: sum.cache_read_tokens + point.cache_read_tokens,
    cache_creation_tokens: sum.cache_creation_tokens + point.cache_creation_tokens
  }), { input_tokens: 0, cache_read_tokens: 0, cache_creation_tokens: 0 }))
})
const rateData = computed(() => ({
  labels: props.trendData.map(point => point.date),
  datasets: [{ label: t('tokens.cacheReadRate'), data: props.trendData.map(point => cacheReadRate(point)), borderColor: '#0ea5e9', tension: 0, spanGaps: false }]
}))
const volumeData = computed(() => ({
  labels: props.trendData.map(point => point.date),
  datasets: [
    { label: t('tokens.input'), data: props.trendData.map(point => point.input_tokens), borderColor: '#f59e0b', yAxisID: 'y', tension: 0 },
    { label: t('dashboard.requests'), data: props.trendData.map(point => point.requests), borderColor: '#8b5cf6', yAxisID: 'requests', tension: 0 }
  ]
}))
const commonOptions = {
  responsive: true, maintainAspectRatio: false,
  interaction: { intersect: false, mode: 'index' as const },
  plugins: { legend: { position: 'top' as const } }
}
const rateOptions = {
  ...commonOptions,
  scales: { y: { min: 0, max: 100, ticks: { callback: (value: number | string) => `${value}%` } } },
  plugins: { ...commonOptions.plugins, tooltip: { callbacks: {
    label: (context: { raw: unknown }) => `${t('tokens.cacheReadRate')}: ${formatCacheReadRate(context.raw as number | null)}`
  } } }
}
const volumeOptions = {
  ...commonOptions,
  scales: {
    y: { beginAtZero: true, ticks: { callback: (value: number | string) => formatTokenCount(Number(value)) } },
    requests: { position: 'right' as const, beginAtZero: true, grid: { drawOnChartArea: false }, ticks: { precision: 0 } }
  }
}
</script>

<template>
  <section class="card p-4" data-testid="cache-usage-trend">
    <h3 class="text-sm font-semibold text-gray-900 dark:text-white">{{ t('tokens.cacheTrend') }}</h3>
    <p class="mt-1 text-xs text-gray-500 dark:text-gray-400">{{ t('tokens.cacheReadRateHelp') }}</p>
    <div v-if="loading" class="flex h-48 items-center justify-center"><LoadingSpinner /></div>
    <template v-else-if="trendData.length">
      <p class="my-3 text-sm font-medium text-gray-900 dark:text-white" data-testid="period-cache-read-rate">{{ t('tokens.cacheReadRate') }}: {{ formatCacheReadRate(rate) }}</p>
      <div class="grid grid-cols-1 gap-4 lg:grid-cols-2">
        <div class="h-56"><Line :data="rateData" :options="rateOptions" /></div>
        <div class="h-56"><Line :data="volumeData" :options="volumeOptions" /></div>
      </div>
      <p class="mt-3 text-xs text-gray-500 dark:text-gray-400">{{ t('tokens.cacheTrendHint') }}</p>
    </template>
    <p v-else class="flex h-48 items-center justify-center text-sm text-gray-500">{{ t('dashboard.noDataAvailable') }}</p>
  </section>
</template>
