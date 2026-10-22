<template>
  <section class="card p-4" :aria-label="t('admin.dashboard.keySpendingRankingTitle')">
    <div class="mb-4 flex flex-wrap items-start justify-between gap-3">
      <div>
        <h3 class="text-sm font-semibold text-gray-900 dark:text-white">
          {{ t('admin.dashboard.keySpendingRankingTitle') }} · Top {{ rankingLimit }}
        </h3>
        <p class="mt-1 text-xs text-gray-500 dark:text-gray-400">
          {{ selectedUser ? selectedUser.email || `#${selectedUser.id}` : t('admin.dashboard.keyRankingGlobal') }}
          · {{ t('admin.dashboard.keyRankingBasis') }}
        </p>
      </div>
      <div class="flex flex-wrap items-center gap-2">
        <button v-if="selectedUser" type="button" class="btn btn-secondary btn-sm" @click="selectUser(null)">
          {{ t('admin.dashboard.keyRankingGlobal') }}
        </button>
        <div ref="searchContainer" class="relative w-full sm:w-auto">
          <form class="flex items-center gap-2" @submit.prevent="searchUsers">
            <input
              v-model="userQuery"
              type="search"
              class="input min-w-0 flex-1 sm:w-60"
              :aria-label="t('admin.usage.searchUserPlaceholder')"
              :placeholder="t('admin.usage.searchUserPlaceholder')"
              @input="clearSearch"
              @keydown.esc="showSearchResults = false"
            />
            <button type="submit" class="btn btn-secondary btn-sm" :disabled="searchLoading || !userQuery.trim()">
              {{ t('common.search') }}
            </button>
          </form>
          <div
            v-if="showSearchResults"
            class="absolute right-0 z-20 mt-1 max-h-64 w-full overflow-y-auto rounded-lg border border-gray-200 bg-white shadow-lg dark:border-dark-600 dark:bg-dark-800"
            aria-live="polite"
          >
            <p v-if="searchLoading" class="p-3 text-sm text-gray-500">{{ t('common.loading') }}</p>
            <p v-else-if="searchError" class="p-3 text-sm text-red-600" role="alert">
              {{ t('admin.dashboard.keyRankingSearchFailed') }}
            </p>
            <ul v-else-if="userResults.length">
              <li v-for="user in userResults" :key="user.id">
                <button
                  type="button"
                  class="w-full break-all px-3 py-2 text-left text-sm hover:bg-gray-100 dark:hover:bg-dark-700"
                  @click="selectUser(user)"
                >
                  {{ user.email }} <span class="text-gray-400">#{{ user.id }}</span>
                </button>
              </li>
            </ul>
            <p v-else class="p-3 text-sm text-gray-500">{{ t('admin.dashboard.noDataAvailable') }}</p>
          </div>
        </div>
      </div>
    </div>

    <div v-if="loading" class="flex h-32 items-center justify-center" aria-live="polite">
      <LoadingSpinner />
    </div>
    <div v-else-if="error" class="flex flex-wrap items-center justify-center gap-3 py-8" role="alert">
      <span class="text-sm text-red-600 dark:text-red-400">{{ t('admin.dashboard.keyRankingFailed') }}</span>
      <button type="button" class="btn btn-secondary btn-sm" @click="loadRanking">
        {{ t('admin.dashboard.keyRankingRetry') }}
      </button>
    </div>
    <template v-else-if="ranking && ranking.ranking.length">
      <p class="mb-3 text-xs text-gray-500 dark:text-gray-400" aria-live="polite">
        {{ t('admin.dashboard.keyRankingScopeTotal') }}:
        <span class="font-medium tabular-nums text-gray-900 dark:text-white">${{ formatCost(ranking.total_actual_cost) }}</span>
        · {{ ranking.total_requests.toLocaleString() }} {{ t('admin.dashboard.requests') }}
        · {{ formatTokenCount(ranking.total_tokens) }} Token
      </p>
      <div class="overflow-x-auto">
        <table class="w-full min-w-[800px] text-sm">
          <thead class="whitespace-nowrap text-xs text-gray-500 dark:text-gray-400">
            <tr>
              <th scope="col" class="pb-2 pr-3 text-left">Key</th>
              <th scope="col" class="pb-2 pr-3 text-left">{{ t('admin.dashboard.spendingRankingUser') }}</th>
              <th scope="col" class="pb-2 pr-3 text-right">{{ t('usage.userBilled') }}</th>
              <th scope="col" class="pb-2 pr-3 text-right">{{ t('admin.dashboard.metricShare') }}</th>
              <th scope="col" class="pb-2 pr-3 text-right">{{ t('admin.dashboard.requests') }}</th>
              <th scope="col" class="pb-2 pr-3 text-right">Token</th>
              <th scope="col" class="whitespace-nowrap pb-2 pr-3 text-right" :title="t('usage.inputCacheReadRateHint')">
                {{ t('usage.inputCacheReadRate') }}
              </th>
              <th scope="col" class="pb-2 text-right">{{ t('admin.dashboard.spendingRankingUsage') }}</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="(item, index) in ranking.ranking" :key="`${item.api_key_id}-${item.user_id}`" class="border-t border-gray-100 dark:border-dark-700">
              <td class="min-w-32 py-3 pr-3">
                <div class="flex items-baseline gap-2">
                  <span class="text-xs tabular-nums text-gray-400">{{ index + 1 }}</span>
                  <div>
                    <button type="button" class="break-all text-left font-medium text-primary-600 hover:underline dark:text-primary-400" @click="goToUsage(item)">
                      {{ item.key_name || `Key #${item.api_key_id}` }}
                    </button>
                    <p class="text-xs text-gray-400">#{{ item.api_key_id }}</p>
                  </div>
                </div>
              </td>
              <td class="min-w-48 py-3 pr-3">
                <button type="button" class="break-all text-left text-gray-600 hover:underline dark:text-gray-300" :title="t('admin.dashboard.keyRankingFilterUser')" @click="selectUser({ id: item.user_id, email: item.email })">
                  {{ item.email || `#${item.user_id}` }}
                </button>
              </td>
              <td class="min-w-24 py-3 pr-3 text-right tabular-nums text-green-600 dark:text-green-400">
                ${{ formatCost(item.actual_cost) }}
                <div class="mt-1 h-1 rounded bg-gray-100 dark:bg-dark-700" aria-hidden="true">
                  <div class="h-1 rounded bg-green-500" :style="{ width: `${costShare(item)}%` }" />
                </div>
              </td>
              <td class="py-3 pr-3 text-right tabular-nums">{{ costShare(item).toFixed(1) }}%</td>
              <td class="py-3 pr-3 text-right tabular-nums">{{ item.requests.toLocaleString() }}</td>
              <td class="py-3 pr-3 text-right tabular-nums">{{ formatTokenCount(item.tokens) }}</td>
              <td class="py-3 pr-3 text-right tabular-nums" :title="tokenBreakdown(item)">
                {{ formatCacheReadRate(cacheReadRate(item)) }}
              </td>
              <td class="py-3 text-right">
                <button type="button" class="btn btn-secondary btn-sm whitespace-nowrap" @click="goToUsage(item)">
                  {{ t('admin.dashboard.keyRankingDetails') }}
                </button>
              </td>
            </tr>
          </tbody>
        </table>
      </div>
    </template>
    <p v-else class="py-8 text-center text-sm text-gray-500 dark:text-gray-400">{{ t('admin.dashboard.noDataAvailable') }}</p>
  </section>
</template>

<script setup lang="ts">
import { onBeforeUnmount, ref, watch } from 'vue'
import { onClickOutside } from '@vueuse/core'
import { useI18n } from 'vue-i18n'
import { useRouter } from 'vue-router'
import { getAPIKeySpendingRanking } from '@/api/admin/dashboard'
import { searchUsers as findUsers, type SimpleUser } from '@/api/admin/usage'
import type { APIKeySpendingRankingItem, APIKeySpendingRankingResponse } from '@/types'
import LoadingSpinner from '@/components/common/LoadingSpinner.vue'
import { cacheReadRate, formatCacheReadRate, formatTokenCount } from '@/utils/usageTokens'

const props = defineProps<{
  startDate?: string
  endDate?: string
  startTime?: string
  endTime?: string
}>()
const { t } = useI18n()
const router = useRouter()
const rankingLimit = 12
const ranking = ref<APIKeySpendingRankingResponse | null>(null)
const loading = ref(false)
const error = ref(false)
const selectedUser = ref<SimpleUser | null>(null)
const userQuery = ref('')
const userResults = ref<SimpleUser[]>([])
const searchLoading = ref(false)
const searchError = ref(false)
const showSearchResults = ref(false)
const searchContainer = ref<HTMLElement | null>(null)
let rankingSeq = 0
let searchSeq = 0

onClickOutside(searchContainer, () => { showSearchResults.value = false })

const rangeParams = () => ({
  start_date: props.startTime ? undefined : props.startDate,
  end_date: props.endTime ? undefined : props.endDate,
  start_time: props.startTime,
  end_time: props.endTime
})

const loadRanking = async () => {
  const seq = ++rankingSeq
  loading.value = true
  error.value = false
  ranking.value = null
  try {
    const result = await getAPIKeySpendingRanking({
      ...rangeParams(), user_id: selectedUser.value?.id, limit: rankingLimit
    })
    if (seq === rankingSeq) ranking.value = result
  } catch {
    if (seq === rankingSeq) error.value = true
  } finally {
    if (seq === rankingSeq) loading.value = false
  }
}

const clearSearch = () => {
  ++searchSeq
  userResults.value = []
  searchLoading.value = false
  searchError.value = false
  showSearchResults.value = false
}

const searchUsers = async () => {
  const query = userQuery.value.trim()
  if (!query) return
  const seq = ++searchSeq
  searchLoading.value = true
  searchError.value = false
  showSearchResults.value = true
  userResults.value = []
  try {
    const users = await findUsers(query)
    if (seq === searchSeq) userResults.value = users
  } catch {
    if (seq === searchSeq) searchError.value = true
  } finally {
    if (seq === searchSeq) searchLoading.value = false
  }
}

const selectUser = (user: SimpleUser | null) => {
  selectedUser.value = user
  userQuery.value = ''
  clearSearch()
}

const goToUsage = (item: APIKeySpendingRankingItem) => {
  void router.push({
    path: '/admin/usage',
    query: { ...rangeParams(), user_id: String(item.user_id), api_key_id: String(item.api_key_id) }
  })
}

const costShare = (item: APIKeySpendingRankingItem) => {
  const total = ranking.value?.total_actual_cost || 0
  return total > 0 ? Math.min(100, Math.max(0, item.actual_cost / total * 100)) : 0
}

const tokenBreakdown = (item: APIKeySpendingRankingItem) =>
  `${t('usage.in')}: ${item.input_tokens.toLocaleString()} / ${t('usage.cacheWrite')}: ${item.cache_creation_tokens.toLocaleString()} / ${t('usage.cacheRead')}: ${item.cache_read_tokens.toLocaleString()}`

const formatCost = (value: number) => value.toFixed(4)

watch(
  () => [props.startDate, props.endDate, props.startTime, props.endTime, selectedUser.value?.id],
  loadRanking,
  { immediate: true }
)
onBeforeUnmount(() => { ++rankingSeq; ++searchSeq })
</script>
