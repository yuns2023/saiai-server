<template>
  <AppLayout>
    <div class="space-y-6">
      <div class="flex flex-wrap items-end justify-between gap-4">
        <div v-if="groups.length" class="w-full sm:w-80">
          <label for="hosted-group" class="input-label">{{ t('hostedAccounts.group') }}</label>
          <select id="hosted-group" :value="selectedGroupID" class="input" :disabled="loadingGroups" @change="selectGroup">
            <option v-for="group in groups" :key="group.id" :value="group.id">{{ group.name }}</option>
          </select>
        </div>
        <button type="button" class="btn btn-secondary" :disabled="loadingGroups || loadingAccounts" @click="refresh">
          {{ t('hostedAccounts.refresh') }}
        </button>
      </div>

      <p class="rounded-lg bg-blue-50 p-4 text-sm text-blue-800 dark:bg-blue-900/20 dark:text-blue-200">
        {{ t('hostedAccounts.notice') }}
      </p>
      <p v-if="error" role="alert" class="text-sm text-red-600 dark:text-red-400">{{ error }}</p>
      <div v-if="loadingGroups || loadingAccounts" role="status" class="py-12 text-center text-gray-500">
        {{ t('hostedAccounts.loading') }}
      </div>
      <div v-else-if="!groups.length && !error" class="card p-12 text-center">
        <h3 class="mb-2 font-semibold">{{ t('hostedAccounts.emptyGroups') }}</h3>
        <p class="text-sm text-gray-500 dark:text-gray-400">{{ t('hostedAccounts.emptyGroupsHint') }}</p>
      </div>
      <div v-else-if="!accounts.length && !error" class="card p-12 text-center text-gray-500">
        {{ t('hostedAccounts.emptyAccounts') }}
      </div>
      <template v-else-if="!error">
        <div class="grid gap-4 lg:grid-cols-2">
          <article v-for="account in accounts" :key="account.id" class="card p-5" data-testid="hosted-account">
            <div class="mb-4 flex items-center justify-between gap-3">
              <h3 class="min-w-0 break-words font-semibold text-gray-900 dark:text-white">{{ account.name }}</h3>
              <span class="shrink-0 rounded bg-gray-100 px-2 py-1 text-xs text-gray-700 dark:bg-dark-700 dark:text-gray-300">
                {{ t(`hostedAccounts.statuses.${account.status}`) }}
              </span>
            </div>
            <p v-if="!account.windows.length" class="py-4 text-sm text-gray-500 dark:text-gray-400">{{ t('hostedAccounts.noData') }}</p>
            <div v-else class="space-y-4">
              <div v-for="window in account.windows" :key="window.key">
                <div class="mb-1 flex justify-between gap-3 text-sm">
                  <span>{{ t(`hostedAccounts.windows.${window.key}`) }}</span>
                  <span class="font-medium">{{ window.utilization.toFixed(1) }}%</span>
                </div>
                <div role="progressbar" :aria-label="t(`hostedAccounts.windows.${window.key}`)" :aria-valuenow="window.utilization" aria-valuemin="0" aria-valuemax="100" class="h-2 overflow-hidden rounded-full bg-gray-100 dark:bg-dark-700">
                  <div class="h-full rounded-full" :class="isStale(account) ? 'bg-gray-400' : window.utilization >= 90 ? 'bg-red-500' : 'bg-primary-500'" :style="{ width: `${Math.min(100, Math.max(0, window.utilization))}%` }"></div>
                </div>
                <p class="mt-1 text-xs text-gray-500 dark:text-gray-400">
                  {{ window.resets_at ? t('hostedAccounts.resetAt', { time: formatTime(window.resets_at) }) : t('hostedAccounts.resetUnknown') }}
                </p>
                <p v-if="window.resets_at" class="mt-1 text-xs text-gray-500 dark:text-gray-400">{{ resetCountdown(window) }}</p>
              </div>
            </div>
            <div class="mt-4 border-t border-gray-100 pt-3 text-xs text-gray-500 dark:border-dark-700 dark:text-gray-400">
              <p>{{ account.updated_at ? t('hostedAccounts.updatedAt', { time: formatTime(account.updated_at) }) : t('hostedAccounts.unknownTime') }}</p>
              <p v-if="account.windows.length && isStale(account)" class="mt-1 text-amber-600 dark:text-amber-400">{{ t('hostedAccounts.stale') }}</p>
            </div>
          </article>
        </div>
        <Pagination v-if="total > 20" :total="total" :page="page" :page-size="20" :show-page-size-selector="false" @update:page="changePage" />
      </template>
    </div>
  </AppLayout>
</template>

<script setup lang="ts">
import { onBeforeUnmount, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { useNow } from '@vueuse/core'
import AppLayout from '@/components/layout/AppLayout.vue'
import Pagination from '@/components/common/Pagination.vue'
import { hostedAccountsAPI, type HostedAccount, type HostedGroup, type HostedQuotaWindow } from '@/api/hosted-accounts'

const { t, locale } = useI18n()
const now = useNow({ interval: 1000 })
const groups = ref<HostedGroup[]>([])
const accounts = ref<HostedAccount[]>([])
const selectedGroupID = ref<number | null>(null)
const loadingGroups = ref(false)
const loadingAccounts = ref(false)
const error = ref('')
const page = ref(1)
const total = ref(0)
let groupsController: AbortController | undefined
let accountsController: AbortController | undefined

const formatTime = (value: string) => new Date(value).toLocaleString(locale.value)
const windowExpired = (window: HostedQuotaWindow) => window.expired ||
  (window.resets_at !== null && new Date(window.resets_at).getTime() <= now.value.getTime())
const isStale = (account: HostedAccount) => account.stale || account.updated_at === null ||
  now.value.getTime() - new Date(account.updated_at).getTime() >= 15 * 60 * 1000 || account.windows.some(windowExpired)
const resetCountdown = (window: HostedQuotaWindow) => {
  if (windowExpired(window)) return t('hostedAccounts.resetElapsed')
  const minutes = Math.ceil((new Date(window.resets_at!).getTime() - now.value.getTime()) / 60000)
  return t('hostedAccounts.remaining', { hours: Math.floor(minutes / 60), minutes: minutes % 60 })
}

async function loadAccounts() {
  accountsController?.abort()
  const controller = new AbortController()
  accountsController = controller
  accounts.value = []
  total.value = 0
  error.value = ''
  if (selectedGroupID.value === null) { loadingAccounts.value = false; return }
  loadingAccounts.value = true
  try {
    const result = await hostedAccountsAPI.list(selectedGroupID.value, page.value, controller.signal)
    if (controller.signal.aborted) return
    accounts.value = result.items
    total.value = result.total
  } catch (cause) {
    if (controller.signal.aborted) return
    const status = (cause as { status?: number; response?: { status: number } }).response?.status ?? (cause as { status?: number }).status
    if (status === 403 || status === 404) {
      groups.value = groups.value.filter(group => group.id !== selectedGroupID.value)
      selectedGroupID.value = null
      error.value = t('hostedAccounts.accessChanged')
    } else {
      error.value = t('hostedAccounts.loadError')
    }
  } finally {
    if (!controller.signal.aborted) loadingAccounts.value = false
  }
}

async function refresh() {
  groupsController?.abort()
  accountsController?.abort()
  const controller = new AbortController()
  groupsController = controller
  loadingGroups.value = true
  loadingAccounts.value = false
  accounts.value = []
  total.value = 0
  error.value = ''
  try {
    const result = await hostedAccountsAPI.groups(controller.signal)
    if (controller.signal.aborted) return
    groups.value = result
    if (!result.some(group => group.id === selectedGroupID.value)) {
      selectedGroupID.value = result[0]?.id ?? null
    }
    page.value = 1
    loadingGroups.value = false
    await loadAccounts()
  } catch {
    if (controller.signal.aborted) return
    groups.value = []
    selectedGroupID.value = null
    error.value = t('hostedAccounts.loadError')
  } finally {
    if (!controller.signal.aborted) loadingGroups.value = false
  }
}

function selectGroup(event: Event) {
  selectedGroupID.value = Number((event.target as HTMLSelectElement).value)
  page.value = 1
  void loadAccounts()
}

function changePage(value: number) {
  page.value = value
  void loadAccounts()
}

onMounted(refresh)
onBeforeUnmount(() => { groupsController?.abort(); accountsController?.abort() })
</script>
