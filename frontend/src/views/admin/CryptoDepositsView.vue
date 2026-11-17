<template>
  <AppLayout>
    <TablePageLayout>
      <template #filters>
        <div class="flex flex-wrap items-center gap-3">
          <p class="flex-1 text-sm text-gray-500 dark:text-dark-400">{{ t('admin.cryptoDeposits.description') }}</p>
          <Select v-model="filter" :options="filterOptions" class="w-36" @change="reload" />
          <button class="btn btn-secondary" :disabled="loading" :title="t('common.refresh')" @click="load">
            <Icon name="refresh" size="md" :class="loading ? 'animate-spin' : ''" />
          </button>
        </div>
      </template>

      <template #table>
        <DataTable :columns="columns" :data="deposits" :loading="loading">
          <template #cell-user="{ row }">
            <div class="text-sm">
              <div class="font-medium text-gray-900 dark:text-white">{{ row.user_name || row.user_email || `#${row.user_id}` }}</div>
              <div v-if="row.user_name && row.user_email" class="text-xs text-gray-500 dark:text-dark-400">{{ row.user_email }}</div>
              <div class="text-xs text-gray-400">#{{ row.user_id }}</div>
              <div v-if="row.user_deleted" class="text-xs text-red-600 dark:text-red-400">{{ t('admin.cryptoDeposits.userDeleted') }}</div>
              <div v-else-if="row.user_status && row.user_status !== 'active'" class="text-xs text-amber-600 dark:text-amber-400">
                {{ t('admin.cryptoDeposits.userNotActive', { status: row.user_status }) }}
              </div>
            </div>
          </template>

          <template #cell-chain="{ row }">
            <div class="text-sm text-gray-900 dark:text-white">{{ row.network }}</div>
            <div class="text-xs text-gray-500 dark:text-dark-400">{{ row.asset }}</div>
          </template>

          <template #cell-amount="{ row }">
            <span class="font-mono text-sm font-medium text-gray-900 dark:text-white">{{ row.amount }} {{ row.asset }}</span>
          </template>

          <template #cell-address="{ row }">
            <a
              v-if="cryptoExplorerUrl(row.explorer_address_url, 'addr', row.address)"
              :href="cryptoExplorerUrl(row.explorer_address_url, 'addr', row.address)"
              target="_blank"
              rel="noopener noreferrer"
              class="font-mono text-xs text-primary-600 hover:underline dark:text-primary-400"
              :title="row.address"
            >{{ shortHash(row.address) }}</a>
            <span v-else class="font-mono text-xs" :title="row.address">{{ shortHash(row.address) }}</span>
          </template>

          <template #cell-tx_hash="{ row }">
            <a
              v-if="cryptoExplorerUrl(row.explorer_tx_url, 'tx', row.tx_hash)"
              :href="cryptoExplorerUrl(row.explorer_tx_url, 'tx', row.tx_hash)"
              target="_blank"
              rel="noopener noreferrer"
              class="font-mono text-xs text-primary-600 hover:underline dark:text-primary-400"
              :title="row.tx_hash"
              data-testid="tx-link"
            >{{ shortHash(row.tx_hash) }}</a>
            <span v-else class="font-mono text-xs" :title="row.tx_hash">{{ shortHash(row.tx_hash) }}</span>
          </template>

          <template #cell-hold_reason="{ row }">
            <span class="text-sm text-gray-700 dark:text-gray-300">{{ holdReasonLabel(row.hold_reason) }}</span>
          </template>

          <template #cell-observed_at="{ row }">
            <span class="text-sm text-gray-500 dark:text-dark-400">{{ formatDateTime(row.observed_at) }}</span>
          </template>

          <template #cell-status="{ row }">
            <span :class="['badge', row.status === 'held' ? 'badge-warning' : row.status === 'credited' ? 'badge-success' : 'badge-gray']">
              {{ statusLabel(row.status) }}
            </span>
            <div v-if="row.status === 'credited' && row.credited_at" class="mt-1 text-xs text-gray-500 dark:text-dark-400">
              {{ formatDateTime(row.credited_at) }}
            </div>
            <div v-if="row.status === 'credited' && row.credited_by" class="text-xs text-gray-400">
              {{ t('admin.cryptoDeposits.releasedBy', { operator: row.credited_by }) }}
            </div>
          </template>

          <template #cell-actions="{ row }">
            <button
              v-if="row.status === 'held'"
              class="btn btn-primary btn-sm"
              :disabled="row.user_deleted || releasing"
              @click="openRelease(row)"
            >
              {{ t('admin.cryptoDeposits.release') }}
            </button>
          </template>

          <template #empty>
            <div class="py-10 text-center text-sm text-gray-500">{{ t('admin.cryptoDeposits.empty') }}</div>
          </template>
        </DataTable>
      </template>

      <template #pagination>
        <Pagination
          v-if="pagination.total > 0"
          :page="pagination.page"
          :total="pagination.total"
          :page-size="pagination.page_size"
          @update:page="handlePageChange"
          @update:pageSize="handlePageSizeChange"
        />
      </template>
    </TablePageLayout>

    <BaseDialog :show="!!releaseTarget" :title="t('admin.cryptoDeposits.releaseTitle')" width="narrow" @close="closeRelease">
      <div v-if="releaseTarget" class="space-y-3 text-sm" data-testid="release-dialog">
        <p class="text-gray-700 dark:text-gray-300">
          {{ t('admin.cryptoDeposits.releaseMessage', {
            amount: releaseTarget.amount,
            asset: releaseTarget.asset,
            chain: releaseTarget.network,
            user: userLabel(releaseTarget)
          }) }}
        </p>
        <p class="text-gray-500 dark:text-dark-400">
          {{ t('admin.cryptoDeposits.columns.holdReason') }}: {{ holdReasonLabel(releaseTarget.hold_reason) }}
          <template v-if="threshold"> · {{ t('admin.cryptoDeposits.releaseThreshold', { amount: threshold }) }}</template>
        </p>
        <p
          v-if="releaseTarget.user_status && releaseTarget.user_status !== 'active'"
          class="rounded-md bg-amber-50 p-2 text-amber-700 dark:bg-amber-900/20 dark:text-amber-300"
          data-testid="release-inactive-warning"
        >
          {{ t('admin.cryptoDeposits.releaseInactiveWarning', { status: releaseTarget.user_status }) }}
        </p>
      </div>
      <template #footer>
        <div class="flex justify-end gap-3">
          <button type="button" class="btn btn-secondary" :disabled="releasing" @click="closeRelease">{{ t('common.cancel') }}</button>
          <button type="button" class="btn btn-primary" :disabled="releasing" data-testid="release-confirm" @click="confirmRelease">
            {{ releasing ? t('admin.cryptoDeposits.releasing') : t('admin.cryptoDeposits.release') }}
          </button>
        </div>
      </template>
    </BaseDialog>
  </AppLayout>
</template>

<script setup lang="ts">
import { computed, onMounted, reactive, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { useAppStore } from '@/stores/app'
import { adminAPI } from '@/api/admin'
import { cryptoExplorerUrl, type CryptoAdminDeposit, type CryptoHoldFilter } from '@/api/admin/cryptoPay'
import { getPersistedPageSize } from '@/composables/usePersistedPageSize'
import { formatDateTime } from '@/utils/format'
import type { Column } from '@/components/common/types'
import AppLayout from '@/components/layout/AppLayout.vue'
import TablePageLayout from '@/components/layout/TablePageLayout.vue'
import DataTable from '@/components/common/DataTable.vue'
import Pagination from '@/components/common/Pagination.vue'
import BaseDialog from '@/components/common/BaseDialog.vue'
import Select from '@/components/common/Select.vue'
import Icon from '@/components/icons/Icon.vue'

const { t, te } = useI18n()
const appStore = useAppStore()

const deposits = ref<CryptoAdminDeposit[]>([])
const loading = ref(false)
const filter = ref<CryptoHoldFilter>('held')
const pagination = reactive({ page: 1, page_size: getPersistedPageSize(), total: 0 })
const threshold = ref('')
const releaseTarget = ref<CryptoAdminDeposit | null>(null)
const releasing = ref(false)
let loadSeq = 0

const filterOptions = computed(() => [
  { value: 'held', label: t('admin.cryptoDeposits.filterHeld') },
  { value: 'released', label: t('admin.cryptoDeposits.filterReleased') },
  { value: 'any', label: t('admin.cryptoDeposits.filterAll') }
])

const columns = computed<Column[]>(() => [
  { key: 'user', label: t('admin.cryptoDeposits.columns.user') },
  { key: 'chain', label: t('admin.cryptoDeposits.columns.chain') },
  { key: 'amount', label: t('admin.cryptoDeposits.columns.amount') },
  { key: 'address', label: t('admin.cryptoDeposits.columns.address') },
  { key: 'tx_hash', label: t('admin.cryptoDeposits.columns.txHash') },
  { key: 'hold_reason', label: t('admin.cryptoDeposits.columns.holdReason') },
  { key: 'observed_at', label: t('admin.cryptoDeposits.columns.detectedAt') },
  { key: 'status', label: t('admin.cryptoDeposits.columns.status') },
  { key: 'actions', label: t('admin.cryptoDeposits.columns.actions') }
])

function shortHash(v: string): string {
  return v && v.length > 16 ? `${v.slice(0, 8)}…${v.slice(-6)}` : v
}

function holdReasonLabel(reason: string): string {
  if (!reason) return '-'
  const key = `admin.cryptoDeposits.holdReasons.${reason}`
  return te(key) ? t(key) : reason
}

function statusLabel(status: string): string {
  const key = `admin.cryptoDeposits.statuses.${status}`
  return te(key) ? t(key) : status
}

function userLabel(d: CryptoAdminDeposit): string {
  const name = d.user_name || d.user_email || `#${d.user_id}`
  return d.user_name && d.user_email ? `${name} <${d.user_email}>` : `${name}`
}

async function load() {
  const seq = ++loadSeq
  loading.value = true
  try {
    const res = await adminAPI.cryptoPay.listDeposits(pagination.page, pagination.page_size, { hold: filter.value })
    if (seq !== loadSeq) return
    const lastPage = Math.max(1, Math.ceil(res.total / pagination.page_size))
    if (res.items.length === 0 && pagination.page > lastPage) {
      // The page emptied (e.g. its last held deposit was released): go to the new last page.
      pagination.page = lastPage
      return load()
    }
    deposits.value = res.items
    pagination.total = res.total
  } catch (err) {
    if (seq !== loadSeq) return
    appStore.showError(errorMessage(err, t('admin.cryptoDeposits.failedToLoad')))
  } finally {
    if (seq === loadSeq) loading.value = false
  }
}

function reload() {
  pagination.page = 1
  void load()
}

function handlePageChange(page: number) {
  pagination.page = page
  void load()
}

function handlePageSizeChange(size: number) {
  pagination.page_size = size
  pagination.page = 1
  void load()
}

function errorMessage(err: unknown, fallback: string): string {
  const e = (err ?? {}) as { reason?: unknown; message?: unknown }
  if (typeof e.reason === 'string') {
    const key = `admin.cryptoDeposits.errors.${e.reason}`
    if (te(key)) return t(key)
  }
  return typeof e.message === 'string' && e.message ? `${fallback}: ${e.message}` : fallback
}

function openRelease(row: CryptoAdminDeposit) {
  if (releasing.value) return
  releaseTarget.value = row
}

function closeRelease() {
  if (releasing.value) return
  releaseTarget.value = null
}

async function confirmRelease() {
  const target = releaseTarget.value
  if (!target || releasing.value) return
  releasing.value = true
  try {
    const out = await adminAPI.cryptoPay.releaseDeposit(target.id)
    if (out.credited) {
      appStore.showSuccess(t('admin.cryptoDeposits.releaseSuccess', { amount: target.amount, asset: target.asset }))
    } else {
      appStore.showInfo(t('admin.cryptoDeposits.releaseAlready'))
    }
    releasing.value = false
    releaseTarget.value = null
  } catch (err) {
    releasing.value = false
    releaseTarget.value = null
    appStore.showError(errorMessage(err, t('admin.cryptoDeposits.releaseFailed')))
  }
  await load()
}

onMounted(async () => {
  void load()
  try {
    const settings = await adminAPI.cryptoPay.getSettings()
    threshold.value = settings?.hold_threshold_amount ?? ''
  } catch {
    // The threshold is informational only.
  }
})
</script>
