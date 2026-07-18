<template>
  <div class="card overflow-hidden">
    <div
      class="flex flex-wrap items-center justify-between gap-3 border-b border-gray-100 px-6 py-4 dark:border-dark-700"
    >
      <div>
        <h2 class="text-lg font-semibold text-gray-900 dark:text-white">
          {{ t('dashboard.apiKeyBreakdown') }}
        </h2>
        <p class="mt-1 text-xs text-gray-500 dark:text-gray-400">
          {{ t('dashboard.apiKeyBreakdownHint') }}
        </p>
      </div>
      <div class="flex items-center gap-3">
        <div class="w-48">
          <Select
            :model-value="sort"
            :options="sortOptions"
            :disabled="loading"
            @update:model-value="onSortChange"
          />
        </div>
        <button
          type="button"
          class="btn btn-secondary"
          :disabled="loading || total === 0"
          @click="emit('export')"
        >
          {{ t('dashboard.exportKeyBreakdown') }}
        </button>
      </div>
    </div>

    <div class="grid grid-cols-2 gap-4 border-b border-gray-100 px-6 py-4 dark:border-dark-700 lg:grid-cols-4">
      <div>
        <p class="text-xs text-gray-500 dark:text-gray-400">{{ t('dashboard.apiKeys') }}</p>
        <p class="mt-1 font-semibold text-gray-900 dark:text-white">{{ total }}</p>
      </div>
      <div>
        <p class="text-xs text-gray-500 dark:text-gray-400">{{ t('dashboard.requests') }}</p>
        <p class="mt-1 font-semibold text-gray-900 dark:text-white">{{ formatNumber(summary.requests) }}</p>
      </div>
      <div>
        <p class="text-xs text-gray-500 dark:text-gray-400">{{ t('dashboard.tokens') }}</p>
        <p class="mt-1 font-semibold text-gray-900 dark:text-white">{{ formatTokens(summary.total_tokens) }}</p>
      </div>
      <div>
        <p class="text-xs text-gray-500 dark:text-gray-400">{{ t('dashboard.actualCost') }}</p>
        <p class="mt-1 font-semibold text-green-600 dark:text-green-400">${{ formatCost(summary.actual_cost) }}</p>
      </div>
    </div>

    <div v-if="loading" class="flex items-center justify-center py-12">
      <LoadingSpinner size="md" />
    </div>
    <div v-else-if="items.length === 0" class="px-6 py-10">
      <EmptyState
        :title="t('dashboard.noApiKeyUsage')"
        :description="t('dashboard.noApiKeyUsageHint')"
      />
    </div>
    <div v-else class="overflow-x-auto">
      <table class="min-w-full divide-y divide-gray-100 text-sm dark:divide-dark-700">
        <thead class="bg-gray-50 text-xs text-gray-500 dark:bg-dark-800/50 dark:text-gray-400">
          <tr>
            <th class="px-6 py-3 text-left font-medium">{{ t('dashboard.apiKey') }}</th>
            <th class="px-4 py-3 text-left font-medium">{{ t('dashboard.status') }}</th>
            <th class="px-4 py-3 text-right font-medium">{{ t('dashboard.requests') }}</th>
            <th class="px-4 py-3 text-right font-medium">{{ t('dashboard.tokens') }}</th>
            <th class="px-4 py-3 text-right font-medium">{{ t('dashboard.actualCost') }}</th>
            <th class="px-4 py-3 text-right font-medium">{{ t('dashboard.usageShare') }}</th>
            <th class="px-4 py-3 text-left font-medium">{{ t('dashboard.lastUsed') }}</th>
            <th class="px-6 py-3 text-right font-medium">{{ t('common.actions') }}</th>
          </tr>
        </thead>
        <tbody class="divide-y divide-gray-100 dark:divide-dark-700">
          <tr v-for="item in items" :key="item.api_key_id">
            <td class="px-6 py-3">
              <div class="font-medium text-gray-900 dark:text-white">{{ item.key_name }}</div>
              <div class="text-xs text-gray-400">#{{ item.api_key_id }}</div>
            </td>
            <td class="px-4 py-3">
              <span class="badge" :class="statusClass(item.status)">{{ statusLabel(item.status) }}</span>
            </td>
            <td class="px-4 py-3 text-right tabular-nums text-gray-700 dark:text-gray-300">
              {{ formatNumber(item.requests) }}
            </td>
            <td class="px-4 py-3 text-right">
              <div class="font-medium tabular-nums text-gray-700 dark:text-gray-300">
                {{ formatTokens(item.total_tokens) }}
              </div>
              <div class="text-xs tabular-nums text-gray-400">
                {{ t('dashboard.input') }} {{ formatTokens(item.input_tokens) }} /
                {{ t('dashboard.output') }} {{ formatTokens(item.output_tokens) }}
              </div>
              <div class="text-xs tabular-nums text-gray-400">
                {{ t('dashboard.cacheWrite') }} {{ formatTokens(item.cache_creation_tokens) }} /
                {{ t('dashboard.cacheRead') }} {{ formatTokens(item.cache_read_tokens) }}
              </div>
            </td>
            <td class="px-4 py-3 text-right">
              <div class="font-medium tabular-nums text-green-600 dark:text-green-400">
                ${{ formatCost(item.actual_cost) }}
              </div>
              <div class="text-xs tabular-nums text-gray-400">
                {{ t('keys.standardCost') }} ${{ formatCost(item.total_cost) }}
              </div>
            </td>
            <td class="px-4 py-3 text-right tabular-nums text-gray-700 dark:text-gray-300">
              {{ formatPercent(item.actual_cost_share) }}
            </td>
            <td class="whitespace-nowrap px-4 py-3 text-gray-500 dark:text-gray-400">
              {{ item.last_used_at ? formatDateTime(item.last_used_at) : '—' }}
            </td>
            <td class="whitespace-nowrap px-6 py-3 text-right">
              <button
                type="button"
                class="mr-3 text-sm font-medium text-primary-600 hover:text-primary-700 dark:text-primary-400"
                @click="emit('select', item.api_key_id)"
              >
                {{ t('dashboard.filterThisKey') }}
              </button>
              <router-link
                :to="usageDetailsTo(item.api_key_id)"
                class="text-sm font-medium text-primary-600 hover:text-primary-700 dark:text-primary-400"
              >
                {{ t('dashboard.viewDetails') }}
              </router-link>
            </td>
          </tr>
        </tbody>
      </table>
    </div>

    <Pagination
      v-if="total > 0"
      :page="page"
      :page-size="pageSize"
      :total="total"
      @update:page="emit('pageChange', $event)"
      @update:pageSize="emit('pageSizeChange', $event)"
    />
  </div>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import type { RouteLocationRaw } from 'vue-router'
import EmptyState from '@/components/common/EmptyState.vue'
import LoadingSpinner from '@/components/common/LoadingSpinner.vue'
import Pagination from '@/components/common/Pagination.vue'
import Select from '@/components/common/Select.vue'
import type {
  APIKeyUsageBreakdownItem,
  APIKeyUsageBreakdownSort,
  APIKeyUsageBreakdownSummary
} from '@/api/usage'
import { formatDateTime } from '@/utils/format'

const props = defineProps<{
  items: APIKeyUsageBreakdownItem[]
  summary: APIKeyUsageBreakdownSummary
  loading: boolean
  total: number
  page: number
  pageSize: number
  startDate: string
  endDate: string
  sort: APIKeyUsageBreakdownSort
}>()

const emit = defineEmits<{
  (event: 'select', apiKeyId: number): void
  (event: 'pageChange', page: number): void
  (event: 'pageSizeChange', pageSize: number): void
  (event: 'sortChange', sort: APIKeyUsageBreakdownSort): void
  (event: 'export'): void
}>()

const { t } = useI18n()
const sortOptions = computed(() => [
  { value: 'actual_cost_desc', label: t('dashboard.sortActualCost') },
  { value: 'requests_desc', label: t('dashboard.sortRequests') },
  { value: 'tokens_desc', label: t('dashboard.sortTokens') },
  { value: 'last_used_desc', label: t('dashboard.sortLastUsed') },
  { value: 'name_asc', label: t('dashboard.sortKeyName') }
])
const validSorts = new Set<APIKeyUsageBreakdownSort>([
  'actual_cost_desc',
  'requests_desc',
  'tokens_desc',
  'last_used_desc',
  'name_asc'
])
const onSortChange = (value: string | number | boolean | null) => {
  if (typeof value === 'string' && validSorts.has(value as APIKeyUsageBreakdownSort)) {
    emit('sortChange', value as APIKeyUsageBreakdownSort)
  }
}

const formatNumber = (value: number) => value.toLocaleString()
const formatCost = (value: number) => value.toFixed(4)
const formatPercent = (value: number) => `${(value * 100).toFixed(1)}%`
const formatTokens = (value: number) => {
  if (value >= 1_000_000_000) return `${(value / 1_000_000_000).toFixed(1)}B`
  if (value >= 1_000_000) return `${(value / 1_000_000).toFixed(1)}M`
  if (value >= 1_000) return `${(value / 1_000).toFixed(1)}K`
  return value.toLocaleString()
}

const statusClass = (status: string) => {
  if (status === 'active') return 'badge-success'
  if (status === 'quota_exhausted') return 'badge-warning'
  if (status === 'expired') return 'badge-danger'
  return 'badge-gray'
}

const statusLabel = (status: string) => {
  const key = `keys.status.${status}`
  const translated = t(key)
  return translated === key ? status : translated
}

const usageDetailsTo = (apiKeyId: number): RouteLocationRaw => ({
  path: '/usage',
  query: {
    api_key_id: String(apiKeyId),
    start_date: props.startDate,
    end_date: props.endDate
  }
})
</script>
