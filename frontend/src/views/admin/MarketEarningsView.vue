<template>
  <AppLayout>
    <div class="space-y-6" data-testid="market-earnings">
      <!-- Owners -->
      <div class="card">
        <div class="flex flex-wrap items-center gap-3 border-b border-gray-100 px-6 py-4 dark:border-dark-700">
          <div class="min-w-[16rem] flex-1">
            <h2 class="text-lg font-semibold text-gray-900 dark:text-white">{{ t('market.earnings.ownersTitle') }}</h2>
            <p class="mt-1 text-sm text-gray-500 dark:text-dark-400">{{ t('market.earnings.description') }}</p>
          </div>
          <form class="flex items-center gap-2" data-testid="owner-filter" @submit.prevent="applyOwnerFilter">
            <input
              v-model.trim="ownerFilterText"
              type="text"
              inputmode="numeric"
              class="input w-36"
              :placeholder="t('market.earnings.ownerIdPlaceholder')"
              data-testid="owner-filter-input"
            />
            <button type="submit" class="btn btn-secondary">{{ t('market.earnings.filter') }}</button>
          </form>
          <button class="btn btn-secondary" :disabled="ownersLoading" :title="t('common.refresh')" data-testid="refresh" @click="refreshAll">
            <Icon name="refresh" size="md" :class="ownersLoading ? 'animate-spin' : ''" />
          </button>
          <button class="btn btn-danger" :disabled="!ownersAsOf" data-testid="reverse-open" @click="openReverse()">{{ t('market.earnings.reverse') }}</button>
        </div>
        <p v-if="ownerFilterInvalid" class="px-6 pt-3 text-sm text-red-600" role="alert">{{ t('market.earnings.ownerIdInvalid') }}</p>
        <p v-if="notAvailable" class="px-6 pt-3 text-sm text-gray-500" data-testid="earnings-not-available">{{ t('market.earnings.notAvailable') }}</p>
        <p v-else-if="ownersError" class="flex items-center gap-3 px-6 pt-3 text-sm text-red-600" role="alert" data-testid="owners-error">
          {{ ownersError }} <button type="button" class="btn btn-secondary btn-sm" @click="loadOwners">{{ t('market.retry') }}</button>
        </p>
        <div class="overflow-x-auto">
          <DataTable :columns="ownerColumns" :data="owners" :loading="ownersLoading" row-key="owner_user_id">
            <template #cell-owner="{ row }">
              <div class="text-sm">
                <div class="font-medium text-gray-900 dark:text-white">{{ row.email || `#${row.owner_user_id}` }}</div>
                <div class="text-xs text-gray-400">#{{ row.owner_user_id }}</div>
              </div>
            </template>
            <template #cell-frozen="{ row }"><span class="font-mono text-sm">{{ usd(row.frozen) }}</span></template>
            <template #cell-available="{ row }"><span class="font-mono text-sm">{{ usd(row.available) }}</span></template>
            <template #cell-converted_total="{ row }"><span class="font-mono text-sm">{{ usd(row.converted_total) }}</span></template>
            <template #cell-reversed_total="{ row }"><span class="font-mono text-sm">{{ usd(row.reversed_total) }}</span></template>
            <template #cell-accrued_total="{ row }"><span class="font-mono text-sm">{{ usd(row.accrued_total) }}</span></template>
            <template #cell-actions="{ row }">
              <div class="flex gap-2">
                <button class="btn btn-secondary btn-sm" :data-testid="`entries-of-${row.owner_user_id}`" @click="showEntriesOf(row.owner_user_id)">
                  {{ t('market.earnings.viewEntries') }}
                </button>
                <button class="btn btn-danger btn-sm" :disabled="!ownersAsOf" :data-testid="`reverse-of-${row.owner_user_id}`" @click="openReverse(row.owner_user_id)">
                  {{ t('market.earnings.reverse') }}
                </button>
              </div>
            </template>
            <template #empty>
              <div class="py-10 text-center text-sm text-gray-500" data-testid="owners-empty">{{ t('market.earnings.ownersEmpty') }}</div>
            </template>
          </DataTable>
        </div>
        <div v-if="pagination.total > 0" class="px-4 py-3">
          <Pagination
            :page="pagination.page"
            :total="pagination.total"
            :page-size="pagination.page_size"
            @update:page="handlePageChange"
            @update:pageSize="handlePageSizeChange"
          />
        </div>
      </div>

      <!-- Ledger -->
      <div class="card" data-testid="entries">
        <div class="flex flex-wrap items-center gap-3 border-b border-gray-100 px-6 py-4 dark:border-dark-700">
          <div class="flex-1">
            <h2 class="text-lg font-semibold text-gray-900 dark:text-white">{{ t('market.earnings.entriesTitle') }}</h2>
            <p class="mt-1 text-sm text-gray-500 dark:text-dark-400">
              {{ entriesOwner ? t('market.earnings.entriesOfOwner', { id: entriesOwner }) : t('market.earnings.entriesAll') }}
            </p>
          </div>
          <button v-if="entriesOwner" class="btn btn-secondary btn-sm" data-testid="entries-all" @click="showEntriesOf(null)">{{ t('market.earnings.showAll') }}</button>
        </div>
        <p v-if="entriesError && !entries.length && !notAvailable" class="flex items-center gap-3 px-6 py-4 text-sm text-red-600" role="alert" data-testid="entries-error">
          {{ entriesError }} <button type="button" class="btn btn-secondary btn-sm" @click="loadEntries(entriesFailedMore)">{{ t('market.retry') }}</button>
        </p>
        <div class="overflow-x-auto">
          <DataTable :columns="entryColumns" :data="entries" :loading="entriesLoading && !entries.length" row-key="id">
            <template #cell-created_at="{ row }"><span class="whitespace-nowrap text-sm text-gray-500">{{ formatDateTime(row.created_at) }}</span></template>
            <template #cell-owner="{ row }"><span class="text-sm">#{{ row.owner_user_id }}</span></template>
            <template #cell-kind="{ row }"><span class="text-sm">{{ kindLabel(row.kind) }}</span></template>
            <template #cell-account="{ row }">
              <span class="text-sm">{{ row.account_id != null ? `#${row.account_id}` : '—' }}</span>
            </template>
            <template #cell-model="{ row }"><span class="font-mono text-xs">{{ row.model || '—' }}</span></template>
            <template #cell-amount="{ row }"><span class="font-mono text-sm">{{ usd(row.amount) }}</span></template>
            <template #cell-status="{ row }">
              <span :class="['badge', entryBadge(row.status)]">{{ statusLabel(row.status) }}</span>
            </template>
            <template #cell-available_at="{ row }">
              <span class="whitespace-nowrap text-sm text-gray-500">{{ row.available_at ? formatDateTime(row.available_at) : '—' }}</span>
            </template>
            <template #cell-ref="{ row }">
              <div class="max-w-[16rem] whitespace-normal text-xs text-gray-500 [overflow-wrap:anywhere]">
                <div v-if="row.settlement_event_id">{{ t('market.earnings.refEvent', { id: row.settlement_event_id }) }}</div>
                <div v-if="row.reverses_entry_id">{{ t('market.earnings.refReverses', { id: row.reverses_entry_id }) }}</div>
                <div v-if="row.admin_user_id">{{ t('market.earnings.refAdmin', { id: row.admin_user_id }) }}</div>
                <div v-if="row.note" class="text-gray-700 dark:text-gray-300">{{ row.note }}</div>
              </div>
            </template>
            <template #empty>
              <div class="py-10 text-center text-sm text-gray-500" data-testid="entries-empty">{{ t('market.earnings.entriesEmpty') }}</div>
            </template>
          </DataTable>
        </div>
        <div v-if="nextCursor != null || (entriesError && entries.length)" class="flex items-center justify-center gap-3 px-4 py-3">
          <p v-if="entriesError && entries.length" class="text-sm text-red-600">{{ entriesError }}</p>
          <button class="btn btn-secondary btn-sm" :disabled="entriesLoading" data-testid="entries-more" @click="loadEntries(entriesError ? entriesFailedMore : true)">
            {{ entriesLoading ? t('common.loading') : entriesError ? t('market.retry') : t('market.earnings.loadMore') }}
          </button>
        </div>
      </div>
    </div>

    <!-- Reverse: scope + required note, then a confirmation -->
    <BaseDialog :show="reverseOpen" :title="t('market.earnings.reverseTitle')" width="normal" :close-on-escape="!busy" @close="closeReverse">
      <form id="reverse-form" class="space-y-4 text-sm" novalidate data-testid="reverse-dialog" @submit.prevent="askReverse">
        <p class="text-gray-600 dark:text-gray-300">{{ t('market.earnings.reverseHint') }}</p>
        <p class="rounded-md bg-amber-50 p-2 text-xs text-amber-700 dark:bg-amber-900/20 dark:text-amber-300" data-testid="reverse-cutoff">
          {{ t('market.earnings.cutoff', { time: formatDateTime(reverseCutoff) }) }}
        </p>
        <fieldset>
          <legend class="input-label">{{ t('market.earnings.scope') }}</legend>
          <div class="flex flex-wrap gap-4">
            <label v-for="sc in REVERSE_SCOPES" :key="sc" class="flex items-center gap-2">
              <input v-model="reverseForm.scope" type="radio" :value="sc" :data-testid="`scope-${sc}`" @change="onScopeChange" />
              {{ t(`market.earnings.scope_${sc}`) }}
            </label>
          </div>
        </fieldset>
        <div v-if="reverseForm.scope !== 'account'">
          <label class="input-label" for="reverse-owner-id">{{ t('market.earnings.ownerId') }}</label>
          <input
            id="reverse-owner-id"
            v-model.trim="reverseForm.ownerId"
            type="text"
            inputmode="numeric"
            :class="['input', reverseTried && !reverseOwnerId && 'input-error']"
            data-testid="reverse-owner-id"
          />
        </div>
        <div v-if="reverseForm.scope !== 'owner'">
          <label class="input-label" for="reverse-account-id">{{ t('market.earnings.accountId') }}</label>
          <input
            id="reverse-account-id"
            v-model.trim="reverseForm.accountId"
            type="text"
            inputmode="numeric"
            :class="['input', reverseTried && !reverseAccountId && 'input-error']"
            data-testid="reverse-account-id"
          />
          <p v-if="reverseForm.scope === 'account'" class="input-hint">{{ t('market.earnings.accountScopeHint') }}</p>
        </div>
        <div>
          <label class="input-label" for="reverse-note">{{ t('market.earnings.note') }}</label>
          <textarea
            id="reverse-note"
            v-model="reverseForm.note"
            rows="3"
            :maxlength="MARKET_REVERSE_NOTE_MAX"
            :class="['input', reverseTried && !reverseForm.note.trim() && 'input-error']"
            data-testid="reverse-note"
          />
          <p class="input-hint">{{ t('market.earnings.noteHint', { max: MARKET_REVERSE_NOTE_MAX }) }}</p>
        </div>
        <p v-if="reverseError" role="alert" class="text-red-600" data-testid="reverse-error">{{ reverseError }}</p>
        <div v-if="reverseResult" class="rounded-md bg-gray-50 p-3 dark:bg-dark-800" data-testid="reverse-result">
          {{ t('market.earnings.reverseDone', { count: reverseResult.reversed_count, amount: usd(reverseResult.reversed_amount) }) }}
        </div>
      </form>
      <template #footer>
        <div class="flex justify-end gap-3">
          <button type="button" class="btn btn-secondary" :disabled="busy" @click="closeReverse">{{ reverseResult ? t('common.close') : t('common.cancel') }}</button>
          <button v-if="!reverseResult" type="submit" form="reverse-form" class="btn btn-danger" :disabled="busy" data-testid="reverse-submit">
            {{ busy ? t('market.earnings.reversing') : t('market.earnings.reverse') }}
          </button>
        </div>
      </template>
    </BaseDialog>

    <ConfirmDialog
      :show="reverseConfirm"
      :title="t('market.earnings.reverseConfirmTitle')"
      :message="reverseConfirmMessage"
      :confirm-text="t('market.earnings.reverse')"
      danger
      @confirm="doReverse"
      @cancel="reverseConfirm = false"
    />
  </AppLayout>
</template>

<script setup lang="ts">
import { computed, onMounted, reactive, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { adminAPI } from '@/api/admin'
import {
  MARKET_REVERSE_NOTE_MAX,
  type AdminOwnerEarningEntry,
  type AdminOwnerEarningSummary,
  type ReverseEarningsResult
} from '@/api/admin/market'
import { getPersistedPageSize } from '@/composables/usePersistedPageSize'
import { useAppStore } from '@/stores/app'
import { formatDateTime } from '@/utils/format'
import { marketErrorText } from '@/utils/market'
import type { Column } from '@/components/common/types'
import AppLayout from '@/components/layout/AppLayout.vue'
import BaseDialog from '@/components/common/BaseDialog.vue'
import ConfirmDialog from '@/components/common/ConfirmDialog.vue'
import DataTable from '@/components/common/DataTable.vue'
import Pagination from '@/components/common/Pagination.vue'
import Icon from '@/components/icons/Icon.vue'

const ENTRY_PAGE = 50

const { t, te } = useI18n()
const appStore = useAppStore()

/* ---------- Helpers ---------- */
function positiveId(text: string): number | null {
  if (!/^\d{1,18}$/.test(text)) return null
  const n = Number(text)
  return Number.isSafeInteger(n) && n > 0 ? n : null
}

/** "$1.20" / "-$0.0000012": exact decimal string, at least 2 places. */
function usd(s: string | null | undefined): string {
  if (typeof s !== 'string' || !/^-?\d+(\.\d+)?$/.test(s.trim())) return '—'
  const v = s.trim()
  const neg = v.startsWith('-')
  const [int, frac = ''] = (neg ? v.slice(1) : v).split('.')
  const f = frac.replace(/0+$/, '').padEnd(2, '0')
  return `${neg ? '-' : ''}$${int.replace(/^0+(?=\d)/, '')}.${f}`
}

function kindLabel(k: string): string {
  return te(`market.earnings.kinds.${k}`) ? t(`market.earnings.kinds.${k}`) : k
}
function statusLabel(s: string): string {
  return te(`market.earnings.statuses.${s}`) ? t(`market.earnings.statuses.${s}`) : s
}
function entryBadge(s: string): string {
  if (s === 'available') return 'badge-success'
  if (s === 'frozen') return 'badge-warning'
  if (s === 'reversed') return 'badge-danger'
  return 'badge-gray'
}

/* ---------- Owners ---------- */
const owners = ref<AdminOwnerEarningSummary[]>([])
/** Server time of the overview on screen: the cutoff of a reverse (only earnings recorded by then). */
const ownersAsOf = ref<string | null>(null)
/** The server has no earnings endpoints (404): an explanation instead of an error. */
const notAvailable = ref(false)
const ownersLoading = ref(false)
const ownersError = ref('')
const pagination = reactive({ page: 1, page_size: getPersistedPageSize(), total: 0 })
const ownerFilterText = ref('')
const ownerFilter = ref<number | undefined>(undefined)
const ownerFilterInvalid = ref(false)
let ownersSeq = 0

const ownerColumns = computed<Column[]>(() => [
  { key: 'owner', label: t('market.earnings.columns.owner') },
  { key: 'frozen', label: t('market.earnings.columns.frozen') },
  { key: 'available', label: t('market.earnings.columns.available') },
  { key: 'converted_total', label: t('market.earnings.columns.converted') },
  { key: 'reversed_total', label: t('market.earnings.columns.reversed') },
  { key: 'accrued_total', label: t('market.earnings.columns.accrued') },
  { key: 'actions', label: t('market.earnings.columns.actions') }
])

async function loadOwners(): Promise<void> {
  const mine = ++ownersSeq
  ownersLoading.value = true
  ownersError.value = ''
  try {
    const page = await adminAPI.market.owners(pagination.page, pagination.page_size, ownerFilter.value)
    if (mine !== ownersSeq) return
    owners.value = page.items
    pagination.total = page.total ?? 0
    // The server may clamp the page or its size: show what it returned.
    if (page.page > 0) pagination.page = page.page
    if (page.page_size > 0) pagination.page_size = page.page_size
    ownersAsOf.value = page.as_of || null
    notAvailable.value = false
  } catch (e) {
    if (mine !== ownersSeq) return
    ownersAsOf.value = null
    notAvailable.value = (e as { status?: unknown } | null)?.status === 404
    ownersError.value = marketErrorText(e, t, t('market.earnings.loadFailed'))
  } finally {
    if (mine === ownersSeq) ownersLoading.value = false
  }
}

function applyOwnerFilter(): void {
  const text = ownerFilterText.value
  const id = text ? positiveId(text) : null
  ownerFilterInvalid.value = !!text && id === null
  if (ownerFilterInvalid.value) return
  ownerFilter.value = id ?? undefined
  pagination.page = 1
  void loadOwners()
  showEntriesOf(id)
}

function handlePageChange(page: number): void {
  pagination.page = page
  void loadOwners()
}
function handlePageSizeChange(size: number): void {
  pagination.page_size = size
  pagination.page = 1
  void loadOwners()
}

/* ---------- Ledger ---------- */
const entries = ref<AdminOwnerEarningEntry[]>([])
const entriesLoading = ref(false)
const entriesError = ref('')
const nextCursor = ref<number | null>(null)
const entriesOwner = ref<number | null>(null)
/** What failed last: the first page (replace) or the next one (append); a retry repeats exactly that. */
const entriesFailedMore = ref(false)
let entriesSeq = 0

const entryColumns = computed<Column[]>(() => [
  { key: 'created_at', label: t('market.earnings.columns.time') },
  { key: 'owner', label: t('market.earnings.columns.owner') },
  { key: 'kind', label: t('market.earnings.columns.kind') },
  { key: 'account', label: t('market.earnings.columns.account') },
  { key: 'model', label: t('market.earnings.columns.model') },
  { key: 'amount', label: t('market.earnings.columns.amount') },
  { key: 'status', label: t('market.earnings.columns.status') },
  { key: 'available_at', label: t('market.earnings.columns.availableAt') },
  { key: 'ref', label: t('market.earnings.columns.ref') }
])

async function loadEntries(more: boolean): Promise<void> {
  const mine = ++entriesSeq
  entriesLoading.value = true
  entriesError.value = ''
  entriesFailedMore.value = more
  try {
    const page = await adminAPI.market.entries({ ownerUserId: entriesOwner.value ?? undefined, cursor: more ? nextCursor.value : null, limit: ENTRY_PAGE })
    if (mine !== entriesSeq) return
    entries.value = more ? [...entries.value, ...page.items] : page.items
    nextCursor.value = page.next_cursor
  } catch (e) {
    if (mine !== entriesSeq) return
    entriesError.value = marketErrorText(e, t, t('market.earnings.loadFailed'))
  } finally {
    if (mine === entriesSeq) entriesLoading.value = false
  }
}

function showEntriesOf(ownerId: number | null): void {
  entriesOwner.value = ownerId
  entries.value = []
  nextCursor.value = null
  void loadEntries(false)
}

function refreshAll(): void {
  void loadOwners()
  void loadEntries(false)
}

/* ---------- Reverse ---------- */
const reverseOpen = ref(false)
const reverseConfirm = ref(false)
const reverseTried = ref(false)
const reverseError = ref('')
const reverseResult = ref<ReverseEarningsResult | null>(null)
const busy = ref(false)
/** owner: all of an owner's earnings; account: every owner's earnings on an account; both: that owner on that account. */
const REVERSE_SCOPES = ['owner', 'account', 'both'] as const
type ReverseScope = (typeof REVERSE_SCOPES)[number]
const reverseForm = reactive({ scope: 'owner' as ReverseScope, ownerId: '', accountId: '', note: '' })
/** Fixed when the dialog opens; a retry of the same dialog keeps it. */
const reverseCutoff = ref<string>('')
const reverseOwnerId = computed(() => positiveId(reverseForm.ownerId))
const reverseAccountId = computed(() => positiveId(reverseForm.accountId))

const reverseConfirmMessage = computed(() =>
  t(`market.earnings.reverseConfirm_${reverseForm.scope}`, {
    owner: reverseForm.ownerId,
    account: reverseForm.accountId,
    time: formatDateTime(reverseCutoff.value)
  })
)

/** A different scope means a different target: the IDs must be entered again. */
function onScopeChange(): void {
  reverseForm.ownerId = ''
  reverseForm.accountId = ''
  reverseTried.value = false
  reverseError.value = ''
}

function openReverse(ownerId?: number): void {
  if (!ownersAsOf.value) return
  reverseCutoff.value = ownersAsOf.value
  reverseForm.scope = 'owner'
  reverseForm.ownerId = ownerId ? String(ownerId) : ''
  reverseForm.accountId = ''
  reverseForm.note = ''
  reverseTried.value = false
  reverseError.value = ''
  reverseResult.value = null
  reverseOpen.value = true
}

function closeReverse(): void {
  if (busy.value) return
  reverseOpen.value = false
}

/** Validates, then asks for confirmation; the note is a UI rule (the server accepts none). */
function askReverse(): void {
  reverseTried.value = true
  reverseError.value = ''
  const needOwner = reverseForm.scope !== 'account'
  const needAccount = reverseForm.scope !== 'owner'
  if ((needOwner && !reverseOwnerId.value) || (needAccount && !reverseAccountId.value)) {
    reverseError.value = t('market.earnings.idRequired')
    return
  }
  if (!reverseForm.note.trim()) {
    reverseError.value = t('market.earnings.noteRequired')
    return
  }
  reverseConfirm.value = true
}

async function doReverse(): Promise<void> {
  reverseConfirm.value = false
  const owner = reverseForm.scope !== 'account' ? reverseOwnerId.value : null
  const account = reverseForm.scope !== 'owner' ? reverseAccountId.value : null
  const note = reverseForm.note.trim()
  const asOf = reverseCutoff.value
  if ((reverseForm.scope !== 'account' && !owner) || (reverseForm.scope !== 'owner' && !account) || !note || !asOf || busy.value) return
  busy.value = true
  reverseError.value = ''
  try {
    const scope = owner && account ? { owner_user_id: owner, account_id: account } : owner ? { owner_user_id: owner } : { account_id: account as number }
    const res = await adminAPI.market.reverse({ ...scope, as_of: asOf, note })
    reverseResult.value = res
    appStore.showSuccess(t('market.earnings.reverseDone', { count: res.reversed_count, amount: usd(res.reversed_amount) }))
    refreshAll()
  } catch (e) {
    reverseError.value = marketErrorText(e, t, t('market.earnings.reverseFailed'))
  } finally {
    busy.value = false
  }
}

onMounted(() => refreshAll())
</script>
