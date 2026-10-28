<template>
  <AppLayout>
    <div class="space-y-6">
      <div v-if="loading && !stats" class="flex items-center justify-center py-12">
        <LoadingSpinner />
      </div>
      <template v-else-if="stats">
        <div class="card p-4">
          <div class="flex flex-wrap items-center gap-4">
            <div class="min-w-[240px]">
              <label class="input-label">{{ t('dashboard.apiKeyFilter') }}</label>
              <Select
                :model-value="selectedAPIKeyID"
                :options="apiKeyOptions"
                :disabled="loadingKeys"
                :searchable="true"
                @update:model-value="onAPIKeyFilterChange"
              />
            </div>
            <div class="min-w-[200px]">
              <label class="input-label">{{ t('dashboard.groupFilter') }}</label>
              <Select
                :model-value="selectedGroupID"
                :options="groupOptions"
                :searchable="true"
                data-testid="dashboard-group-filter"
                @update:model-value="onGroupFilterChange"
              />
            </div>
            <p class="text-sm text-gray-500 dark:text-gray-400">
              {{ t('dashboard.apiKeyFilterHint') }}
            </p>
          </div>
        </div>

        <UserDashboardStats
          :stats="stats"
          :balance="user?.balance || 0"
          :is-simple="authStore.isSimpleMode"
        />
        <UserDashboardCharts
          v-model:start-date="startDate"
          v-model:end-date="endDate"
          v-model:granularity="granularity"
          :loading="loadingCharts"
          :trend="trendData"
          :models="modelStats"
          @date-range-change="loadRangeData"
          @granularity-change="loadCharts"
        />
        <UserDashboardAPIKeyBreakdown
          :items="keyBreakdownItems"
          :summary="keyBreakdownSummary"
          :loading="loadingKeyBreakdown"
          :total="keyBreakdownPagination.total"
          :page="keyBreakdownPagination.page"
          :page-size="keyBreakdownPagination.pageSize"
          :start-date="startDate"
          :end-date="endDate"
          :sort="keyBreakdownSort"
          @select="selectAPIKey"
          @page-change="changeKeyBreakdownPage"
          @page-size-change="changeKeyBreakdownPageSize"
          @sort-change="changeKeyBreakdownSort"
          @export="exportKeyBreakdown"
        />
        <div class="grid grid-cols-1 gap-6 lg:grid-cols-3">
          <div class="lg:col-span-2">
            <UserDashboardRecentUsage
              :data="recentUsage"
              :loading="loadingUsage"
              :range-label="`${startDate} – ${endDate}`"
              :view-all-to="usageDetailsRoute"
            />
          </div>
          <div class="lg:col-span-1"><UserDashboardQuickActions /></div>
        </div>
      </template>
    </div>
  </AppLayout>
</template>

<script setup lang="ts">
import { computed, onMounted, reactive, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import type { RouteLocationRaw } from 'vue-router'
import { useAuthStore } from '@/stores/auth'
import { useAppStore } from '@/stores/app'
import { keysAPI } from '@/api/keys'
import {
  usageAPI,
  type APIKeyUsageBreakdownItem,
  type APIKeyUsageBreakdownParams,
  type APIKeyUsageBreakdownSort,
  type APIKeyUsageBreakdownSummary,
  type UsageGroupOption,
  type UserDashboardStats as UserStatsType
} from '@/api/usage'
import AppLayout from '@/components/layout/AppLayout.vue'
import LoadingSpinner from '@/components/common/LoadingSpinner.vue'
import Select from '@/components/common/Select.vue'
import UserDashboardStats from '@/components/user/dashboard/UserDashboardStats.vue'
import UserDashboardCharts from '@/components/user/dashboard/UserDashboardCharts.vue'
import UserDashboardRecentUsage from '@/components/user/dashboard/UserDashboardRecentUsage.vue'
import UserDashboardQuickActions from '@/components/user/dashboard/UserDashboardQuickActions.vue'
import UserDashboardAPIKeyBreakdown from '@/components/user/dashboard/UserDashboardAPIKeyBreakdown.vue'
import type { UsageLog, TrendDataPoint, ModelStat } from '@/types'
import { escapeCsvCell } from '@/utils/csv'

const { t } = useI18n()
const authStore = useAuthStore()
const appStore = useAppStore()
const user = computed(() => authStore.user)

const stats = ref<UserStatsType | null>(null)
const loading = ref(false)
const loadingKeys = ref(false)
const loadingUsage = ref(false)
const loadingCharts = ref(false)
const loadingKeyBreakdown = ref(false)
const trendData = ref<TrendDataPoint[]>([])
const modelStats = ref<ModelStat[]>([])
const recentUsage = ref<UsageLog[]>([])
const selectedAPIKeyID = ref<number | null>(null)
const apiKeyOptions = ref<Array<{ value: number | null; label: string }>>([])
// Final groups seen in this user's usage for the chart range; a selected group
// missing from a new range stays selectable so the filter holds.
const selectedGroupID = ref<number | null>(null)
const usageGroups = ref<UsageGroupOption[]>([])
const groupOptions = computed(() => {
  const options: Array<{ value: number | null; label: string }> = [
    { value: null, label: t('dashboard.allGroups') },
    ...usageGroups.value.map((group) => ({ value: group.id, label: group.name || `#${group.id}` }))
  ]
  const selected = selectedGroupID.value
  if (selected && !usageGroups.value.some((group) => group.id === selected)) {
    options.push({ value: selected, label: `#${selected}` })
  }
  return options
})
const keyBreakdownSort = ref<APIKeyUsageBreakdownSort>('actual_cost_desc')

const keyBreakdownItems = ref<APIKeyUsageBreakdownItem[]>([])
const keyBreakdownSummary = ref<APIKeyUsageBreakdownSummary>({
  requests: 0,
  total_tokens: 0,
  total_cost: 0,
  actual_cost: 0
})
const keyBreakdownPagination = reactive({
  page: 1,
  pageSize: 20,
  total: 0,
  pages: 1
})

const formatLocalDate = (date: Date) =>
  `${date.getFullYear()}-${String(date.getMonth() + 1).padStart(2, '0')}-${String(date.getDate()).padStart(2, '0')}`
const today = new Date()
const sixDaysAgo = new Date(today)
sixDaysAgo.setDate(sixDaysAgo.getDate() - 6)
const browserTimezone = Intl.DateTimeFormat().resolvedOptions().timeZone || undefined
const startDate = ref(formatLocalDate(sixDaysAgo))
const endDate = ref(formatLocalDate(today))
const granularity = ref<'day' | 'hour' | '15m'>('day')

const selectedAPIKey = computed(() => selectedAPIKeyID.value ?? undefined)
const selectedGroup = computed(() => selectedGroupID.value ?? undefined)
const usageDetailsRoute = computed<RouteLocationRaw>(() => ({
  path: '/usage',
  query: {
    ...(selectedAPIKeyID.value ? { api_key_id: String(selectedAPIKeyID.value) } : {}),
    ...(selectedGroupID.value ? { group_id: String(selectedGroupID.value) } : {}),
    start_date: startDate.value,
    end_date: endDate.value
  }
}))

const loadAPIKeyOptions = async () => {
  loadingKeys.value = true
  try {
    const options: Array<{ value: number | null; label: string }> = [
      { value: null, label: t('dashboard.allApiKeys') }
    ]
    let page = 1
    let pages = 1
    do {
      const response = await keysAPI.list(page, 100)
      options.push(
        ...response.items.map((key) => ({
          value: key.id,
          label: `${key.name} (#${key.id})`
        }))
      )
      pages = response.pages
      page += 1
    } while (page <= pages)
    apiKeyOptions.value = options
  } catch (error) {
    console.error('Failed to load API Key options:', error)
    apiKeyOptions.value = [{ value: null, label: t('dashboard.allApiKeys') }]
  } finally {
    loadingKeys.value = false
  }
}

// Each loader applies only its latest response, so quick filter changes cannot
// leave cards or tables showing an earlier selection.
const requestSeq = { stats: 0, charts: 0, recent: 0, breakdown: 0, groups: 0 }

const loadStats = async () => {
  const seq = ++requestSeq.stats
  loading.value = true
  try {
    const response = await usageAPI.getDashboardStats(selectedAPIKey.value, selectedGroup.value)
    if (seq === requestSeq.stats) stats.value = response
  } catch (error) {
    console.error('Failed to load dashboard stats:', error)
  } finally {
    if (seq === requestSeq.stats) loading.value = false
  }
}

const loadCharts = async () => {
  const seq = ++requestSeq.charts
  loadingCharts.value = true
  try {
    const [trend, models] = await Promise.all([
      usageAPI.getDashboardTrend({
        start_date: startDate.value,
        end_date: endDate.value,
        granularity: granularity.value,
        api_key_id: selectedAPIKey.value,
        group_id: selectedGroup.value,
        timezone: browserTimezone
      }),
      usageAPI.getDashboardModels({
        start_date: startDate.value,
        end_date: endDate.value,
        api_key_id: selectedAPIKey.value,
        group_id: selectedGroup.value,
        timezone: browserTimezone
      })
    ])
    if (seq !== requestSeq.charts) return
    trendData.value = trend.trend || []
    modelStats.value = models.models || []
  } catch (error) {
    console.error('Failed to load dashboard charts:', error)
  } finally {
    if (seq === requestSeq.charts) loadingCharts.value = false
  }
}

const loadRecent = async () => {
  const seq = ++requestSeq.recent
  loadingUsage.value = true
  try {
    const response = await usageAPI.getByDateRange(
      startDate.value,
      endDate.value,
      selectedAPIKey.value,
      browserTimezone,
      selectedGroup.value
    )
    if (seq === requestSeq.recent) recentUsage.value = response.items.slice(0, 5)
  } catch (error) {
    console.error('Failed to load recent usage:', error)
  } finally {
    if (seq === requestSeq.recent) loadingUsage.value = false
  }
}

const loadKeyBreakdown = async () => {
  const seq = ++requestSeq.breakdown
  loadingKeyBreakdown.value = true
  try {
    const response = await usageAPI.getDashboardAPIKeyBreakdown({
      start_date: startDate.value,
      end_date: endDate.value,
      group_id: selectedGroup.value,
      timezone: browserTimezone,
      page: keyBreakdownPagination.page,
      page_size: keyBreakdownPagination.pageSize,
      sort: keyBreakdownSort.value
    })
    if (seq !== requestSeq.breakdown) return
    keyBreakdownItems.value = response.items || []
    keyBreakdownSummary.value = response.summary
    keyBreakdownPagination.total = response.total
    keyBreakdownPagination.pages = response.pages
  } catch (error) {
    console.error('Failed to load API Key usage breakdown:', error)
  } finally {
    if (seq === requestSeq.breakdown) loadingKeyBreakdown.value = false
  }
}

const loadGroupOptions = async () => {
  const seq = ++requestSeq.groups
  try {
    const groups = await usageAPI.getUsageGroups(startDate.value, endDate.value, browserTimezone)
    if (seq === requestSeq.groups) usageGroups.value = groups
  } catch (error) {
    console.error('Failed to load usage group options:', error)
  }
}

const loadRangeData = () => {
  keyBreakdownPagination.page = 1
  void Promise.all([loadCharts(), loadRecent(), loadKeyBreakdown(), loadGroupOptions()])
}

const onGroupFilterChange = (value: string | number | boolean | null) => {
  if (typeof value === 'boolean') return
  const parsed = value === null || value === '' ? null : Number(value)
  if (parsed !== null && !(Number.isSafeInteger(parsed) && parsed > 0)) return
  selectedGroupID.value = parsed
  keyBreakdownPagination.page = 1
  void Promise.all([loadStats(), loadCharts(), loadRecent(), loadKeyBreakdown()])
}

const selectAPIKey = (apiKeyID: number | null) => {
  selectedAPIKeyID.value = apiKeyID
  void Promise.all([loadStats(), loadCharts(), loadRecent()])
}

const onAPIKeyFilterChange = (value: string | number | boolean | null) => {
  if (value === null || value === '') {
    selectAPIKey(null)
    return
  }
  if (typeof value === 'boolean') return
  const parsed = Number(value)
  if (Number.isSafeInteger(parsed) && parsed > 0) {
    selectAPIKey(parsed)
  }
}

const changeKeyBreakdownPage = (page: number) => {
  keyBreakdownPagination.page = page
  void loadKeyBreakdown()
}

const changeKeyBreakdownPageSize = (pageSize: number) => {
  keyBreakdownPagination.pageSize = pageSize
  keyBreakdownPagination.page = 1
  void loadKeyBreakdown()
}

const changeKeyBreakdownSort = (sort: APIKeyUsageBreakdownSort) => {
  keyBreakdownSort.value = sort
  keyBreakdownPagination.page = 1
  void loadKeyBreakdown()
}

const fetchAllKeyBreakdownRows = async (
  query: Omit<APIKeyUsageBreakdownParams, 'page' | 'page_size'>
) => {
  const rows: APIKeyUsageBreakdownItem[] = []
  let page = 1
  let pages = 1
  do {
    const response = await usageAPI.getDashboardAPIKeyBreakdown({ ...query, page, page_size: 100 })
    rows.push(...response.items)
    pages = response.pages
    page += 1
  } while (page <= pages)
  return rows
}

const exportKeyBreakdown = async () => {
  // Freeze the query so filter changes mid-export cannot mix result sets.
  const query = {
    start_date: startDate.value,
    end_date: endDate.value,
    group_id: selectedGroup.value,
    timezone: browserTimezone,
    sort: keyBreakdownSort.value
  }
  try {
    const rows = await fetchAllKeyBreakdownRows(query)
    const headers = [
      'API Key ID',
      'API Key Name',
      'Status',
      'Last Used At',
      'Requests',
      'Input Tokens',
      'Output Tokens',
      'Cache Creation Tokens',
      'Cache Read Tokens',
      'Total Tokens',
      'Actual Cost',
      'Standard Cost',
      'Actual Cost Share'
    ]
    const csvRows = rows.map((item) =>
      [
        item.api_key_id,
        item.key_name,
        item.status,
        item.last_used_at || '',
        item.requests,
        item.input_tokens,
        item.output_tokens,
        item.cache_creation_tokens,
        item.cache_read_tokens,
        item.total_tokens,
        item.actual_cost.toFixed(8),
        item.total_cost.toFixed(8),
        (item.actual_cost_share * 100).toFixed(4) + '%'
      ].map(escapeCsvCell)
    )
    const csv = [headers.map(escapeCsvCell).join(','), ...csvRows.map((row) => row.join(','))].join(
      '\n'
    )
    const blob = new Blob([csv], { type: 'text/csv;charset=utf-8;' })
    const url = window.URL.createObjectURL(blob)
    const link = document.createElement('a')
    link.href = url
    link.download = `api-key-usage_${query.start_date}_to_${query.end_date}.csv`
    link.click()
    window.URL.revokeObjectURL(url)
    appStore.showSuccess(t('dashboard.keyBreakdownExported'))
  } catch (error) {
    console.error('Failed to export API Key usage breakdown:', error)
    appStore.showError(t('dashboard.keyBreakdownExportFailed'))
  }
}

onMounted(() => {
  void authStore.refreshUser()
  void loadAPIKeyOptions()
  void loadGroupOptions()
  void Promise.all([loadStats(), loadCharts(), loadRecent(), loadKeyBreakdown()])
})
</script>
