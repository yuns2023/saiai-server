<template>
  <div class="card overflow-hidden" data-testid="usage-analytics">
    <div
      class="flex flex-wrap items-center justify-between gap-3 border-b border-gray-100 px-6 py-4 dark:border-dark-700"
    >
      <div>
        <h2 class="text-lg font-semibold text-gray-900 dark:text-white">
          {{ t('dashboard.analytics.title') }}
        </h2>
        <p class="mt-1 text-xs text-gray-500 dark:text-gray-400">
          {{ t('dashboard.analytics.hint', { start: startDate, end: endDate }) }}
        </p>
      </div>
      <div class="flex flex-wrap items-center gap-2">
        <button
          type="button"
          class="btn btn-secondary"
          data-testid="analytics-api-example"
          @click="showApiExample = true"
        >
          {{ t('dashboard.analytics.apiExample') }}
        </button>
        <button
          type="button"
          class="btn btn-secondary"
          data-testid="analytics-export"
          :disabled="exporting || loading || !result || result.total === 0"
          @click="exportCsv"
        >
          {{ exporting ? t('dashboard.analytics.exporting') : t('dashboard.analytics.exportCsv') }}
        </button>
      </div>
    </div>

    <!-- Extra filters (date range, Key and group come from the dashboard filters) -->
    <div
      class="grid grid-cols-1 gap-3 border-b border-gray-100 px-6 py-4 sm:grid-cols-2 lg:grid-cols-5 dark:border-dark-700"
    >
      <div>
        <label class="input-label">{{ t('dashboard.analytics.modelFilter') }}</label>
        <Select
          :model-value="modelFilter"
          :options="modelOptions"
          :searchable="true"
          :creatable="true"
          :creatable-prefix="t('dashboard.analytics.exactModel')"
          :aria-label="t('dashboard.analytics.modelFilter')"
          @update:model-value="onModelFilterChange"
        />
      </div>
      <div>
        <label class="input-label">{{ t('dashboard.analytics.effortFilter') }}</label>
        <Select
          :model-value="effortFilter"
          :options="effortOptions"
          :aria-label="t('dashboard.analytics.effortFilter')"
          @update:model-value="onEffortFilterChange"
        />
      </div>
      <div>
        <label class="input-label">{{ t('dashboard.analytics.requestType') }}</label>
        <Select
          :model-value="requestTypeFilter"
          :options="requestTypeOptions"
          :aria-label="t('dashboard.analytics.requestType')"
          @update:model-value="onRequestTypeFilterChange"
        />
      </div>
      <div>
        <label class="input-label">{{ t('dashboard.analytics.billingType') }}</label>
        <Select
          :model-value="billingTypeFilter"
          :options="billingTypeOptions"
          :aria-label="t('dashboard.analytics.billingType')"
          @update:model-value="onBillingTypeFilterChange"
        />
      </div>
      <div>
        <label class="input-label">{{ t('dashboard.analytics.sort') }}</label>
        <Select
          :model-value="sort"
          :options="sortOptions"
          :aria-label="t('dashboard.analytics.sort')"
          @update:model-value="onSortChange"
        />
      </div>
    </div>

    <!-- Dimension tabs + share metric -->
    <div
      class="flex flex-wrap items-center justify-between gap-3 border-b border-gray-100 px-6 py-3 dark:border-dark-700"
    >
      <div class="flex flex-wrap gap-1" role="group" :aria-label="t('dashboard.analytics.dimension')">
        <button
          v-for="tab in dimensionTabs"
          :key="tab.value"
          type="button"
          :aria-pressed="groupBy === tab.value"
          :data-testid="`analytics-tab-${tab.value}`"
          class="rounded-lg px-3 py-1.5 text-sm font-medium transition-colors"
          :class="groupBy === tab.value
            ? 'bg-primary-50 text-primary-700 dark:bg-primary-900/30 dark:text-primary-300'
            : 'text-gray-500 hover:bg-gray-100 hover:text-gray-700 dark:text-gray-400 dark:hover:bg-dark-700 dark:hover:text-gray-200'"
          @click="groupBy = tab.value"
        >
          {{ tab.label }}
        </button>
      </div>
      <div class="flex items-center gap-2">
        <span class="text-xs text-gray-500 dark:text-gray-400">{{ t('dashboard.analytics.shareBy') }}</span>
        <div
          class="inline-flex rounded-lg border border-gray-200 bg-gray-50 p-0.5 dark:border-gray-700 dark:bg-dark-800"
          role="group"
          :aria-label="t('dashboard.analytics.shareBy')"
        >
          <button
            v-for="option in metricOptions"
            :key="option.value"
            type="button"
            :data-testid="`analytics-metric-${option.value}`"
            :aria-pressed="metric === option.value"
            class="rounded-md px-2.5 py-1 text-xs font-medium transition-colors"
            :class="metric === option.value
              ? 'bg-white text-gray-900 shadow-xs dark:bg-dark-700 dark:text-white'
              : 'text-gray-500 hover:text-gray-700 dark:text-gray-400 dark:hover:text-gray-200'"
            @click="metric = option.value"
          >
            {{ option.label }}
          </button>
        </div>
      </div>
    </div>

    <div v-if="error" class="px-6 py-10 text-center" role="alert" data-testid="analytics-error">
      <p class="text-sm text-red-600 dark:text-red-400">{{ error }}</p>
      <button type="button" class="btn btn-secondary mt-3" @click="load">
        {{ t('common.refresh') }}
      </button>
    </div>
    <div
      v-else-if="!result"
      class="flex items-center justify-center py-12"
      role="status"
      :aria-label="t('common.loading')"
      aria-busy="true"
    >
      <LoadingSpinner size="md" />
    </div>
    <div v-else class="relative" :class="loading ? 'opacity-60' : ''" :aria-busy="loading">
      <div
        v-if="loading"
        class="absolute inset-0 z-10 flex items-center justify-center"
        role="status"
        :aria-label="t('common.loading')"
      >
        <LoadingSpinner size="md" />
      </div>

      <!-- Summary over the whole filtered result -->
      <div class="border-b border-gray-100 px-6 py-4 dark:border-dark-700" data-testid="analytics-summary">
        <div class="grid grid-cols-2 gap-4 sm:grid-cols-3 lg:grid-cols-5">
          <div>
            <p class="text-xs text-gray-500 dark:text-gray-400">{{ t('dashboard.requests') }}</p>
            <p class="mt-1 font-semibold tabular-nums text-gray-900 dark:text-white">
              {{ formatNumber(result.summary.requests) }}
            </p>
          </div>
          <div>
            <p class="text-xs text-gray-500 dark:text-gray-400">{{ t('tokens.total') }}</p>
            <p class="mt-1 font-semibold text-gray-900 dark:text-white">
              <TokenCount :value="result.summary.total_tokens" :breakdown="summaryTokens" />
            </p>
          </div>
          <div>
            <p class="flex items-center text-xs text-gray-500 dark:text-gray-400">
              {{ t('dashboard.analytics.actualCost') }}
              <HelpTooltip :content="t('dashboard.analytics.actualCostHelp')" />
            </p>
            <p class="mt-1 font-semibold tabular-nums text-green-600 dark:text-green-400">
              ${{ formatCost(result.summary.actual_cost) }}
            </p>
          </div>
          <div>
            <p class="flex items-center text-xs text-gray-500 dark:text-gray-400">
              {{ t('dashboard.analytics.standardCost') }}
              <HelpTooltip :content="t('dashboard.analytics.standardCostHelp')" />
            </p>
            <p class="mt-1 font-semibold tabular-nums text-gray-900 dark:text-white">
              ${{ formatCost(result.summary.cost) }}
            </p>
          </div>
          <div v-if="showSummaryUsageCost" data-testid="analytics-usage-cost">
            <p class="flex items-center text-xs text-gray-500 dark:text-gray-400">
              {{ t('dashboard.analytics.usageCost') }}
              <HelpTooltip :content="t('dashboard.analytics.usageCostHelp')" />
            </p>
            <p class="mt-1 font-semibold tabular-nums text-gray-900 dark:text-white">
              ${{ formatCost(result.summary.usage_cost) }}
            </p>
          </div>
        </div>
        <div class="mt-4">
          <p class="mb-1.5 text-xs text-gray-500 dark:text-gray-400">{{ t('dashboard.analytics.tokenMix') }}</p>
          <UsageTokenMixBar :mix="result.summary.token_mix" :data="result.summary" legend />
        </div>
      </div>

      <div v-if="rows.length === 0" class="px-6 py-10">
        <EmptyState
          :title="t('dashboard.analytics.empty')"
          :description="t('dashboard.analytics.emptyHint')"
        />
      </div>
      <template v-else>
        <!-- Reasoning-effort distribution -->
        <div
          v-if="result.group_by === 'reasoning_effort'"
          class="space-y-2 border-b border-gray-100 px-4 py-4 sm:px-6 dark:border-dark-700"
          data-testid="analytics-effort-distribution"
        >
          <div v-for="item in effortChartRows" :key="analyticsRowKey(item)" class="flex items-center gap-3 text-sm">
            <div class="flex w-20 shrink-0 items-center truncate text-gray-700 sm:w-32 dark:text-gray-300" data-testid="analytics-effort-chart-label">
              {{ effortLabel(item) }}
              <HelpTooltip
                v-if="item.reasoning_effort_missing"
                :content="t('dashboard.analytics.effortMissingHelp')"
              />
            </div>
            <div class="h-3 flex-1 overflow-hidden rounded-full bg-gray-100 dark:bg-dark-700">
              <div
                class="h-full rounded-full bg-primary-500"
                :style="{ width: shareBarWidth(rowShare(item)) }"
              ></div>
            </div>
            <div class="w-14 shrink-0 text-right tabular-nums text-gray-700 sm:w-20 dark:text-gray-300">
              {{ formatAnalyticsShare(rowShare(item)) }}
            </div>
          </div>
        </div>

        <div class="overflow-x-auto">
          <table class="min-w-full divide-y divide-gray-100 text-sm dark:divide-dark-700">
            <thead class="bg-gray-50 text-xs text-gray-500 dark:bg-dark-800/50 dark:text-gray-400">
              <tr>
                <th v-if="showsKey" class="px-4 py-3 text-left font-medium whitespace-nowrap xl:whitespace-normal">
                  {{ t('dashboard.apiKey') }}
                </th>
                <th
                  v-if="showsModel"
                  class="whitespace-nowrap py-3 text-left font-medium"
                  :class="showsKey ? 'px-3' : 'px-4'"
                >
                  {{ t('dashboard.model') }}
                </th>
                <th v-if="result.group_by === 'reasoning_effort'" class="px-4 py-3 text-left font-medium whitespace-nowrap xl:whitespace-normal">
                  {{ t('usage.reasoningEffort') }}
                </th>
                <th class="px-3 py-3 text-right font-medium whitespace-nowrap xl:whitespace-normal">{{ t('dashboard.requests') }}</th>
                <th class="px-3 py-3 text-right font-medium whitespace-nowrap xl:whitespace-normal">{{ t('tokens.total') }}</th>
                <th class="px-3 py-3 text-right font-medium whitespace-nowrap xl:whitespace-normal">{{ t('dashboard.analytics.actualCost') }}</th>
                <th class="px-3 py-3 text-right font-medium whitespace-nowrap xl:whitespace-normal">{{ t('dashboard.analytics.standardCost') }}</th>
                <th class="px-3 py-3 text-right font-medium whitespace-nowrap xl:whitespace-normal">{{ shareColumnLabel }}</th>
                <th class="px-3 py-3 text-left font-medium whitespace-nowrap xl:whitespace-normal">{{ t('dashboard.analytics.tokenMix') }}</th>
                <th v-if="showsKey" class="px-3 py-3 text-right font-medium whitespace-nowrap xl:whitespace-normal">{{ t('common.actions') }}</th>
              </tr>
            </thead>
            <tbody class="divide-y divide-gray-100 dark:divide-dark-700" data-testid="analytics-rows">
              <tr v-for="item in rows" :key="analyticsRowKey(item)">
                <td v-if="showsKey" class="px-4 py-3">
                  <div class="flex flex-wrap items-center gap-x-2 gap-y-1">
                    <span class="max-w-[9rem] break-words font-medium text-gray-900 dark:text-white" :title="item.api_key_name">
                      {{ item.api_key_name || '—' }}
                    </span>
                    <span v-if="item.api_key_deleted" class="badge badge-gray" data-testid="analytics-key-deleted">
                      {{ t('dashboard.analytics.deleted') }}
                    </span>
                  </div>
                  <div class="text-xs text-gray-400">#{{ item.api_key_id }}</div>
                </td>
                <td v-if="showsModel" class="py-3" :class="showsKey ? 'px-3' : 'px-4'">
                  <span class="block max-w-[11rem] break-all font-medium text-gray-900 dark:text-white" :title="item.model">
                    {{ item.model || '—' }}
                  </span>
                </td>
                <td v-if="result.group_by === 'reasoning_effort'" class="px-4 py-3">
                  <span
                    class="inline-flex items-center whitespace-nowrap font-medium"
                    :class="item.reasoning_effort_missing ? 'text-gray-500 dark:text-gray-400' : 'text-gray-900 dark:text-white'"
                    data-testid="analytics-effort-label"
                  >
                    {{ effortLabel(item) }}
                    <HelpTooltip
                      v-if="item.reasoning_effort_missing"
                      lazy
                      :content="t('dashboard.analytics.effortMissingHelp')"
                    />
                  </span>
                </td>
                <td class="px-3 py-3 text-right tabular-nums text-gray-700 dark:text-gray-300">
                  {{ formatNumber(item.requests) }}
                </td>
                <td class="px-3 py-3 text-right text-gray-700 dark:text-gray-300">
                  <TokenCount :value="item.total_tokens" :breakdown="tokenBreakdown(item)" />
                </td>
                <td class="whitespace-nowrap px-3 py-3 text-right">
                  <div class="font-medium tabular-nums text-green-600 dark:text-green-400">
                    ${{ formatCost(item.actual_cost) }}
                  </div>
                  <div v-if="differsFromActual(item)" class="text-xs tabular-nums text-gray-400">
                    {{ t('dashboard.analytics.usageCostShort') }} ${{ formatCost(item.usage_cost) }}
                  </div>
                </td>
                <td class="whitespace-nowrap px-3 py-3 text-right tabular-nums text-gray-500 dark:text-gray-400">
                  ${{ formatCost(item.cost) }}
                </td>
                <td class="px-3 py-3 text-right" data-testid="analytics-share">
                  <div class="tabular-nums text-gray-700 dark:text-gray-300">
                    {{ formatAnalyticsShare(rowShare(item)) }}
                  </div>
                  <div class="ml-auto mt-1 h-1 w-16 overflow-hidden rounded-full bg-gray-100 dark:bg-dark-700">
                    <div class="h-full bg-primary-500" :style="{ width: shareBarWidth(rowShare(item)) }"></div>
                  </div>
                </td>
                <td class="px-3 py-3">
                  <UsageTokenMixBar :mix="item.token_mix" :data="item" class="w-20" />
                </td>
                <td v-if="showsKey" class="px-3 py-3 text-right">
                  <router-link
                    v-if="item.api_key_id"
                    :to="usageDetailsTo(item.api_key_id)"
                    :aria-label="keyRecordsLinkLabel(item)"
                    data-testid="analytics-key-records"
                    class="inline-block max-w-[6rem] text-sm font-medium text-primary-600 hover:text-primary-700 dark:text-primary-400"
                  >
                    {{ t('dashboard.analytics.viewKeyRecords') }}
                  </router-link>
                </td>
              </tr>
            </tbody>
          </table>
        </div>
      </template>

      <Pagination
        v-if="result.total > PAGE_SIZE"
        :page="page"
        :page-size="PAGE_SIZE"
        :total="Math.min(result.total, MAX_PAGE * PAGE_SIZE)"
        :show-page-size-selector="false"
        @update:page="changePage"
      />
    </div>

    <BaseDialog
      :show="showApiExample"
      :title="t('dashboard.analytics.apiDialogTitle')"
      width="wide"
      @close="showApiExample = false"
    >
      <div class="space-y-3 text-sm text-gray-700 dark:text-gray-300" data-testid="analytics-api-dialog">
        <p>{{ t('dashboard.analytics.apiDialogIntro') }}</p>
        <div>
          <div class="mb-1.5 flex justify-end">
            <button
              type="button"
              class="btn btn-secondary btn-sm"
              @click="copyToClipboard(curlExample)"
            >
              {{ copied ? t('common.copied') : t('dashboard.analytics.copy') }}
            </button>
          </div>
          <pre
            class="whitespace-pre-wrap break-all rounded-lg bg-gray-900 p-4 text-xs leading-relaxed text-gray-100"
            data-testid="analytics-curl"
          ><code>{{ curlExample }}</code></pre>
        </div>
        <p class="text-xs text-gray-500 dark:text-gray-400">{{ t('dashboard.analytics.apiDialogTokenNote') }}</p>
        <p class="text-xs text-gray-500 dark:text-gray-400">{{ t('dashboard.analytics.apiDialogParamsNote') }}</p>
        <p class="text-xs text-gray-500 dark:text-gray-400">{{ t('dashboard.analytics.apiDialogPerKeyNote') }}</p>
      </div>
    </BaseDialog>
  </div>
</template>

<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import type { RouteLocationRaw } from 'vue-router'
import BaseDialog from '@/components/common/BaseDialog.vue'
import EmptyState from '@/components/common/EmptyState.vue'
import HelpTooltip from '@/components/common/HelpTooltip.vue'
import LoadingSpinner from '@/components/common/LoadingSpinner.vue'
import Pagination from '@/components/common/Pagination.vue'
import Select from '@/components/common/Select.vue'
import TokenCount from '@/components/common/TokenCount.vue'
import UsageTokenMixBar from './UsageTokenMixBar.vue'
import {
  usageAPI,
  usageAnalyticsEndpointURL,
  type UsageAnalyticsGroupBy,
  type UsageAnalyticsItem,
  type UsageAnalyticsParams,
  type UsageAnalyticsResponse,
  type UsageAnalyticsSort
} from '@/api/usage'
import { useAppStore } from '@/stores/app'
import { useClipboard } from '@/composables/useClipboard'
import {
  formatCostFixed as formatCost,
  formatNumberLocaleString as formatNumber,
  formatReasoningEffort
} from '@/utils/format'
import { tokenBreakdown } from '@/utils/usageTokens'
import {
  analyticsCurlExample,
  analyticsRowKey,
  analyticsShare,
  buildUsageAnalyticsCsv,
  costShareField,
  formatAnalyticsShare,
  dedupeAnalyticsRows,
  orderEffortRows,
  shareBarWidth,
  type UsageAnalyticsMetric
} from '@/utils/usageAnalytics'

const props = defineProps<{
  startDate: string
  endDate: string
  apiKeyId?: number | null
  groupId?: number | null
  timezone?: string
  /** Model names seen in the dashboard model breakdown, offered as filter options. */
  models?: string[]
}>()

const PAGE_SIZE = 50
/** Backend page limit. */
const MAX_PAGE = 10000
const EXPORT_PAGE_SIZE = 1000
const EXPORT_MAX_PAGES = 10
const EFFORT_MISSING = '__missing__'
const EFFORT_VALUES = ['none', 'minimal', 'low', 'medium', 'high', 'xhigh', 'max']

const { t } = useI18n()
const appStore = useAppStore()
const { copied, copyToClipboard } = useClipboard()

const groupBy = ref<UsageAnalyticsGroupBy>('model')
const sort = ref<UsageAnalyticsSort>('actual_cost_desc')
const metric = ref<UsageAnalyticsMetric>('cost')
const page = ref(1)
const modelFilter = ref<string | null>(null)
const effortFilter = ref<string | null>(null)
const requestTypeFilter = ref<UsageAnalyticsParams['request_type'] | null>(null)
const billingTypeFilter = ref<0 | 1 | null>(null)

const result = ref<UsageAnalyticsResponse | null>(null)
const loading = ref(false)
const error = ref('')
const exporting = ref(false)
const showApiExample = ref(false)

const dimensionTabs = computed<Array<{ value: UsageAnalyticsGroupBy; label: string }>>(() => [
  { value: 'model', label: t('dashboard.analytics.byModel') },
  { value: 'reasoning_effort', label: t('dashboard.analytics.byEffort') },
  { value: 'api_key', label: t('dashboard.analytics.byApiKey') },
  { value: 'api_key_model', label: t('dashboard.analytics.byApiKeyModel') }
])
const metricOptions = computed<Array<{ value: UsageAnalyticsMetric; label: string }>>(() => [
  { value: 'cost', label: t('dashboard.analytics.metricCost') },
  { value: 'tokens', label: t('dashboard.analytics.metricTokens') },
  { value: 'requests', label: t('dashboard.analytics.metricRequests') }
])
const sortOptions = computed(() => [
  { value: 'actual_cost_desc', label: t('dashboard.sortActualCost') },
  { value: 'cost_desc', label: t('dashboard.analytics.sortStandardCost') },
  { value: 'usage_cost_desc', label: t('dashboard.analytics.sortUsageCost') },
  { value: 'tokens_desc', label: t('dashboard.sortTokens') },
  { value: 'requests_desc', label: t('dashboard.sortRequests') }
])
const modelOptions = computed(() => {
  const names = [...new Set((props.models ?? []).filter(Boolean))]
  if (modelFilter.value && !names.includes(modelFilter.value)) names.unshift(modelFilter.value)
  return [
    { value: null, label: t('dashboard.analytics.allModels') },
    ...names.map((name) => ({ value: name, label: name }))
  ]
})
const effortOptions = computed(() => [
  { value: null, label: t('dashboard.analytics.allEfforts') },
  ...EFFORT_VALUES.map((value) => ({ value, label: formatReasoningEffort(value) })),
  { value: EFFORT_MISSING, label: t('dashboard.analytics.effortMissing') }
])
const requestTypeOptions = computed(() => [
  { value: null, label: t('dashboard.analytics.allRequestTypes') },
  { value: 'sync', label: t('usage.sync') },
  { value: 'stream', label: t('usage.stream') },
  { value: 'ws_v2', label: t('usage.ws') },
  { value: 'unknown', label: t('usage.unknown') }
])
const billingTypeOptions = computed(() => [
  { value: null, label: t('dashboard.analytics.allBillingTypes') },
  { value: 0, label: t('usage.balanceCharge') },
  { value: 1, label: t('usage.subscriptionUsage') }
])

const validSorts = new Set<string>(['actual_cost_desc', 'cost_desc', 'usage_cost_desc', 'tokens_desc', 'requests_desc'])
const onSortChange = (value: string | number | boolean | null) => {
  if (typeof value === 'string' && validSorts.has(value)) sort.value = value as UsageAnalyticsSort
}
const onModelFilterChange = (value: string | number | boolean | null) => {
  modelFilter.value = typeof value === 'string' && value.trim() ? value.trim() : null
}
const onEffortFilterChange = (value: string | number | boolean | null) => {
  effortFilter.value = typeof value === 'string' && value ? value : null
}
const onRequestTypeFilterChange = (value: string | number | boolean | null) => {
  const valid = ['sync', 'stream', 'ws_v2', 'unknown'] as const
  requestTypeFilter.value = valid.find((item) => item === value) ?? null
}
const onBillingTypeFilterChange = (value: string | number | boolean | null) => {
  billingTypeFilter.value = value === 0 || value === 1 ? value : null
}

// Everything except paging; any change here restarts at page 1.
const baseParams = computed<UsageAnalyticsParams>(() => ({
  start_date: props.startDate,
  end_date: props.endDate,
  timezone: props.timezone || undefined,
  group_by: groupBy.value,
  sort: sort.value,
  api_key_id: props.apiKeyId ?? undefined,
  group_id: props.groupId ?? undefined,
  model: modelFilter.value ?? undefined,
  reasoning_effort:
    effortFilter.value && effortFilter.value !== EFFORT_MISSING ? effortFilter.value : undefined,
  reasoning_effort_missing: effortFilter.value === EFFORT_MISSING ? true : undefined,
  request_type: requestTypeFilter.value ?? undefined,
  billing_type: billingTypeFilter.value ?? undefined
}))
const filterKey = computed(() => JSON.stringify(baseParams.value))

// Only the latest request may update the view; superseded ones are aborted.
let requestSeq = 0
let controller: AbortController | null = null
let exportController: AbortController | null = null
const load = async () => {
  const seq = ++requestSeq
  controller?.abort()
  const current = new AbortController()
  controller = current
  loading.value = true
  error.value = ''
  try {
    const response = await usageAPI.getUsageAnalytics(
      { ...baseParams.value, page: page.value, page_size: PAGE_SIZE },
      { signal: current.signal }
    )
    if (seq !== requestSeq) return
    result.value = response
  } catch (err) {
    if (seq !== requestSeq) return
    console.error('Failed to load usage analytics:', err)
    result.value = null
    error.value = (err as { message?: string })?.message || t('dashboard.analytics.loadFailed')
  } finally {
    if (seq === requestSeq) {
      loading.value = false
      controller = null
    }
  }
}

watch(
  filterKey,
  () => {
    page.value = 1
    void load()
  },
  { immediate: true }
)
onBeforeUnmount(() => {
  requestSeq += 1
  controller?.abort()
  exportController?.abort()
})

const changePage = (next: number) => {
  const clamped = Math.min(MAX_PAGE, Math.max(1, Math.trunc(next) || 1))
  if (clamped === page.value) return
  page.value = clamped
  void load()
}

// The table keeps the server order (selected sort); only the effort chart lists "Not recorded" last.
const rows = computed(() => result.value?.items ?? [])
const effortChartRows = computed(() => orderEffortRows(rows.value))
const showsKey = computed(() => result.value?.group_by === 'api_key' || result.value?.group_by === 'api_key_model')
const showsModel = computed(() => result.value?.group_by === 'model' || result.value?.group_by === 'api_key_model')
const summaryTokens = computed(() => tokenBreakdown(result.value?.summary))
const differsFromActual = (metrics: { actual_cost: number; usage_cost: number }) =>
  Math.abs(metrics.usage_cost - metrics.actual_cost) > 1e-9
const showSummaryUsageCost = computed(() => !!result.value && differsFromActual(result.value.summary))
const usesUsageCostShare = computed(
  () => !!result.value && costShareField(result.value.summary) === 'usage_cost'
)
const shareColumnLabel = computed(() => {
  if (metric.value === 'tokens') return t('dashboard.analytics.shareTokens')
  if (metric.value === 'requests') return t('dashboard.analytics.shareRequests')
  return usesUsageCostShare.value ? t('dashboard.analytics.shareUsageCost') : t('dashboard.analytics.shareCost')
})
const rowShare = (item: UsageAnalyticsItem) =>
  result.value ? analyticsShare(item, result.value.summary, metric.value) : null
const effortLabel = (item: UsageAnalyticsItem) =>
  item.reasoning_effort_missing
    ? t('dashboard.analytics.effortMissing')
    : formatReasoningEffort(item.reasoning_effort)

// The records page only filters by Key, group and dates, so the link says exactly that.
const keyRecordsLinkLabel = (item: UsageAnalyticsItem) => {
  const key = item.api_key_name || `#${item.api_key_id}`
  return item.model !== undefined && result.value?.group_by === 'api_key_model'
    ? t('dashboard.analytics.viewKeyRecordsForModelRow', { key, model: item.model || '—' })
    : t('dashboard.analytics.viewKeyRecordsFor', { key })
}

const usageDetailsTo = (apiKeyId: number): RouteLocationRaw => ({
  path: '/usage',
  query: {
    api_key_id: String(apiKeyId),
    ...(props.groupId ? { group_id: String(props.groupId) } : {}),
    start_date: props.startDate,
    end_date: props.endDate
  }
})

const curlExample = computed(() =>
  analyticsCurlExample(usageAnalyticsEndpointURL(), {
    ...baseParams.value,
    page: page.value,
    page_size: PAGE_SIZE
  })
)

const exportCsv = async () => {
  // Freeze filters so changes mid-export cannot mix result sets.
  const query = { ...baseParams.value }
  const groupByAtStart = groupBy.value
  const current = new AbortController()
  exportController = current
  exporting.value = true
  try {
    const items: UsageAnalyticsItem[] = []
    let nextPage = 1
    let pages = 1
    let fetchedPages = 0
    do {
      const response = await usageAPI.getUsageAnalytics(
        { ...query, page: nextPage, page_size: EXPORT_PAGE_SIZE },
        { signal: current.signal }
      )
      items.push(...(response.items || []))
      pages = response.pages
      fetchedPages += 1
      nextPage += 1
    } while (nextPage <= pages && nextPage <= EXPORT_MAX_PAGES)
    if (current.signal.aborted) return
    // Rankings can shift between page requests (e.g. today's usage still growing),
    // so a group may appear on two pages; keep its first occurrence.
    const rows = dedupeAnalyticsRows(items)
    const csv = buildUsageAnalyticsCsv(groupByAtStart, rows)
    // BOM so Excel reads UTF-8 (Chinese Key names), like the admin export.
    const blob = new Blob(['\uFEFF', csv], { type: 'text/csv;charset=utf-8;' })
    const url = window.URL.createObjectURL(blob)
    const link = document.createElement('a')
    link.href = url
    link.download = `usage-analytics_${groupByAtStart}_${query.start_date}_to_${query.end_date}.csv`
    link.click()
    window.URL.revokeObjectURL(url)
    if (pages > fetchedPages) {
      appStore.showWarning(t('dashboard.analytics.exportTruncated', { count: rows.length }))
    } else if (fetchedPages > 1) {
      appStore.showWarning(t('dashboard.analytics.exportMultiPage'))
    } else {
      appStore.showSuccess(t('dashboard.analytics.exported'))
    }
  } catch (err) {
    if (current.signal.aborted) return
    console.error('Failed to export usage analytics:', err)
    appStore.showError(t('dashboard.analytics.exportFailed'))
  } finally {
    if (exportController === current) exportController = null
    exporting.value = false
  }
}
</script>
