<script setup lang="ts">
/**
 * 共享收益: what my shared accounts earned (frozen / available / converted),
 * the conversion of available earnings into ordinary balance, and the ledger.
 * The ledger names only my own account and model — never a buyer.
 *
 * Conversion is idempotent on the server by (me, request_id). A conversion
 * (request id + amount) is recorded before it is sent — in the hosted store
 * and in localStorage per user — and replayed unchanged by every retry: a
 * timeout, 429/401/403/5xx, reopening the dialog, leaving the page, reloading
 * it. It ends only with a definite answer: success (also a replay) or a
 * business error that decides this very request (see DEFINITE_ERRORS); only
 * then can a new one start. The request in flight is tracked in the store, so
 * a second mount cannot send another.
 */
import { computed, onMounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { marketApi, type OwnerEarningEntry, type OwnerEarnings } from '@/api/market'
import ConfirmDialog from '@/components/ConfirmDialog.vue'
import { intlLocale } from '@/i18n'
import { dateTime, shortTime } from '@/lib/format'
import { canConvert, checkConvertAmount, floorToCents, marketErrorKey, newRequestId, usdExact } from '@/lib/market'
import { useAppStore } from '@/stores/app'
import { useAuthStore } from '@/stores/auth'
import { useHostedStore } from '@/stores/hosted'

const PAGE_SIZE = 20

const props = defineProps<{ earnings: OwnerEarnings }>()
const { t, te } = useI18n()
const app = useAppStore()
const auth = useAuthStore()
const hosted = useHostedStore()

/* ---------- Conversion ---------- */
const open = ref(false)
const amount = ref('')
const error = ref('')
/** One conversion at a time across page visits (the request outlives this component). */
const busy = computed(() => hosted.convertBusy)
/** A conversion whose outcome is unknown: the next attempt replays it with the same id and amount. */
const pending = computed(() => hosted.pendingConvert)

const convertible = computed(() => canConvert(props.earnings) || pending.value !== null)
const maxAmount = computed(() => floorToCents(props.earnings.available))

/**
 * The recorded conversion this dialog was opened to retry (null: opened for a
 * new one). If that conversion is settled meanwhile (another page visit got
 * the answer), confirming must not turn into a new conversion.
 */
const dialogTxn = ref<string | null>(null)

function openDialog(): void {
  dialogTxn.value = pending.value?.requestId ?? null
  amount.value = pending.value?.amount ?? ''
  error.value = ''
  open.value = true
}

function fillAll(): void {
  amount.value = maxAmount.value
  error.value = ''
}

/**
 * Answers that decide this request for good: nothing was converted under this
 * id (amount / id rejected, not enough available, balance limit) or the id
 * was already used for another amount. Everything else — 429, 401/403, 5xx,
 * network, timeout — leaves the outcome open: keep the id and amount.
 */
const DEFINITE_ERRORS: ReadonlySet<string> = new Set([
  'MARKET_EARNING_CONVERT_CONFLICT',
  'MARKET_EARNING_INSUFFICIENT',
  'MARKET_EARNING_INVALID_AMOUNT',
  'MARKET_EARNING_BELOW_MINIMUM',
  'MARKET_EARNING_INVALID_REQUEST_ID',
  'BALANCE_OUT_OF_RANGE'
])

function definite(e: unknown): boolean {
  const x = (e ?? {}) as { reason?: unknown; code?: unknown }
  return [x.reason, x.code].some((v) => typeof v === 'string' && DEFINITE_ERRORS.has(v))
}

async function submit(): Promise<void> {
  if (!open.value || hosted.convertBusy) return
  if (dialogTxn.value !== null && hosted.pendingConvert?.requestId !== dialogTxn.value) {
    // The conversion this dialog retries was settled elsewhere: nothing to send.
    open.value = false
    return
  }
  let txn = hosted.pendingConvert
  if (txn && dialogTxn.value === null) {
    // Another visit recorded a conversion after this dialog opened: show it instead of sending a different one.
    dialogTxn.value = txn.requestId
    amount.value = txn.amount
    return
  }
  if (!txn) {
    const checked = checkConvertAmount(amount.value, props.earnings.min_convert, props.earnings.available)
    if ('problem' in checked) {
      error.value = t(`earnings.amount_${checked.problem}`, { min: usdExact(props.earnings.min_convert), max: usdExact(maxAmount.value) })
      return
    }
    // Recorded before sending: until the server answers definitely, every retry (also after leaving the page) replays exactly this.
    txn = { requestId: newRequestId(), amount: checked.amount }
    hosted.setPendingConvert(txn)
  }
  const current = txn
  dialogTxn.value = current.requestId
  error.value = ''
  hosted.convertBusy = true
  const session = hosted.session()
  // Only the callback of the conversion still recorded may settle it (the watch below then refreshes every mounted page).
  const settle = () => {
    if (dialogTxn.value === current.requestId) dialogTxn.value = null
    if (hosted.pendingConvert?.requestId === current.requestId) hosted.setPendingConvert(null)
  }
  try {
    const res = await marketApi.convert(current.amount, current.requestId)
    if (hosted.session() !== session) return
    settle()
    open.value = false
    app.notify('ok', t('earnings.converted', { amount: usdExact(res.amount) }))
    // The balance shown everywhere (also when this page was left meanwhile).
    auth.refreshUser().catch(() => undefined)
  } catch (e) {
    if (hosted.session() !== session) return
    const key = marketErrorKey(e)
    if (definite(e)) {
      // Decided: nothing was converted under this id, or (conflict) it was already used — never reuse it.
      settle()
      amount.value = ''
      error.value = te(key) ? t(key) : t('market.errors.generic')
    } else {
      error.value = t('earnings.unconfirmed')
    }
  } finally {
    if (hosted.session() === session) hosted.convertBusy = false
  }
}

// Whichever page visit got the answer, the card on screen shows the result: a settled
// conversion closes a dialog that was retrying it and reloads the ledger (the page reloads the summary).
watch(
  () => hosted.pendingConvert?.requestId ?? null,
  (now, prev) => {
    if (!prev || now === prev) return
    // dialogTxn stays: a late confirm still sees that its conversion is settled.
    if (open.value && dialogTxn.value === prev) open.value = false
    void reloadEntries()
  }
)

/* ---------- Ledger ---------- */
const rows = ref<OwnerEarningEntry[]>([])
const nextCursor = ref<number | null>(null)
const loading = ref(false)
const failed = ref(false)
const failedText = ref('')
/** What failed last: the first page (replace) or the next one (append); a retry repeats exactly that. */
const failedMore = ref(false)
let seq = 0

async function loadEntries(more = false): Promise<void> {
  const mine = ++seq
  loading.value = true
  failed.value = false
  failedMore.value = more
  try {
    const page = await marketApi.entries(more ? nextCursor.value : null, PAGE_SIZE)
    if (mine !== seq) return
    rows.value = more ? [...rows.value, ...page.items] : page.items
    nextCursor.value = page.next_cursor
  } catch (e) {
    if (mine !== seq) return
    failed.value = true
    const key = marketErrorKey(e)
    failedText.value = key !== 'market.errors.generic' && te(key) ? t(key) : t('common.loadFailed')
  } finally {
    if (mine === seq) loading.value = false
  }
}

function reloadEntries(): Promise<void> {
  return loadEntries(false)
}

function kindLabel(k: string): string {
  return te(`earnings.kind_${k}`) ? t(`earnings.kind_${k}`) : k
}
function statusLabel(s: string): string {
  return te(`earnings.status_${s}`) ? t(`earnings.status_${s}`) : s
}
function statusTag(s: string): string {
  if (s === 'available') return 'tag-ok'
  if (s === 'frozen') return 'tag-warn'
  return 'tag-off'
}

onMounted(() => {
  hosted.loadPendingConvert(auth.user?.id)
  void reloadEntries()
})
</script>

<template>
  <section class="mt-12" aria-labelledby="earnings-title" data-testid="earnings">
    <h2 id="earnings-title" class="text-md font-semibold">{{ t('earnings.title') }}</h2>
    <p class="mt-1 max-w-[70ch] text-xs text-ink-3">{{ t('earnings.lead') }}</p>

    <div class="rule-double mt-4 flex flex-wrap items-end justify-between gap-x-10 gap-y-4 pb-5">
      <dl class="grid grid-cols-3 gap-x-8 gap-y-2" data-testid="earnings-summary">
        <div>
          <dt class="label">{{ t('earnings.frozen') }}</dt>
          <dd class="num mt-1 text-md" data-testid="earnings-frozen">{{ usdExact(earnings.frozen) }}</dd>
        </div>
        <div>
          <dt class="label">{{ t('earnings.available') }}</dt>
          <dd class="num mt-1 text-md font-semibold" data-testid="earnings-available">{{ usdExact(earnings.available) }}</dd>
        </div>
        <div>
          <dt class="label">{{ t('earnings.convertedTotal') }}</dt>
          <dd class="num mt-1 text-md" data-testid="earnings-converted">{{ usdExact(earnings.converted_total) }}</dd>
        </div>
      </dl>
      <div class="flex flex-col items-start gap-1 sm:items-end">
        <button type="button" class="btn btn-primary btn-sm" :disabled="!convertible || busy" data-testid="earnings-convert" @click="openDialog">
          {{ t('earnings.convert') }}
        </button>
        <span class="text-xs text-ink-3">{{ t('earnings.minHint', { min: usdExact(earnings.min_convert) }) }}</span>
      </div>
    </div>

    <!-- Ledger -->
    <div class="mt-4">
      <p v-if="failed && !rows.length" class="py-6 text-sm text-seal" role="alert" data-testid="earnings-entries-error">
        {{ failedText }} <button type="button" class="btn btn-quiet btn-sm" @click="reloadEntries">{{ t('common.retry') }}</button>
      </p>
      <p v-else-if="!loading && !rows.length" class="border-b border-rule-soft py-8 text-center text-sm text-ink-2" data-testid="earnings-empty">
        {{ t('earnings.empty') }}
      </p>
      <template v-else>
        <div class="relative overflow-x-auto">
          <table class="ledger sm:min-w-[44rem]" data-testid="earnings-table">
            <thead>
              <tr>
                <th class="w-[7.5rem]">{{ t('earnings.time') }}</th>
                <th>{{ t('earnings.kind') }}</th>
                <th class="hidden sm:table-cell">{{ t('earnings.account') }}</th>
                <th class="hidden sm:table-cell">{{ t('earnings.model') }}</th>
                <th class="amount">{{ t('earnings.amount') }}</th>
                <th>{{ t('earnings.status') }}</th>
                <th class="hidden md:table-cell">{{ t('earnings.availableAt') }}</th>
              </tr>
            </thead>
            <tbody v-if="loading && !rows.length">
              <tr v-for="i in 3" :key="i">
                <td colspan="7"><div class="skeleton h-4 w-full" /></td>
              </tr>
            </tbody>
            <tbody v-else>
              <tr v-for="r in rows" :key="r.id" data-testid="earnings-row">
                <td class="num whitespace-nowrap text-ink-2" :title="dateTime(r.created_at, intlLocale())">{{ shortTime(r.created_at, intlLocale()) }}</td>
                <td class="whitespace-nowrap">{{ kindLabel(r.kind) }}</td>
                <td class="hidden whitespace-nowrap text-ink-2 sm:table-cell">{{ r.account_id != null ? t('earnings.accountRef', { id: r.account_id }) : '—' }}</td>
                <td class="hidden max-w-[12rem] truncate font-mono text-xs leading-5 sm:table-cell" :title="r.model ?? ''">{{ r.model || '—' }}</td>
                <td class="amount num font-medium">{{ usdExact(r.amount) }}</td>
                <td><span class="tag" :class="statusTag(r.status)">{{ statusLabel(r.status) }}</span></td>
                <td class="hidden num whitespace-nowrap text-ink-2 md:table-cell" :title="dateTime(r.available_at, intlLocale())">{{ r.available_at ? shortTime(r.available_at, intlLocale()) : '—' }}</td>
              </tr>
            </tbody>
          </table>
        </div>
        <div v-if="nextCursor != null || (failed && rows.length)" class="flex items-center justify-center gap-3 pt-4">
          <p v-if="failed" class="text-sm text-seal" role="alert">{{ failedText }}</p>
          <button type="button" class="btn btn-line btn-sm" :disabled="loading" data-testid="earnings-more" @click="loadEntries(failed ? failedMore : true)">
            {{ loading ? t('hosted.loadingMore') : failed ? t('common.retry') : t('hosted.loadMore') }}
          </button>
        </div>
      </template>
    </div>

    <ConfirmDialog
      :open="open"
      :title="t('earnings.convertTitle')"
      :body="t('earnings.convertBody')"
      :confirm-label="busy ? t('earnings.converting') : t('earnings.convertConfirm')"
      :busy="busy"
      @confirm="submit"
      @cancel="open = false"
    >
      <form class="mt-4" novalidate @submit.prevent="submit">
        <label class="block">
          <span class="label">{{ t('earnings.amountLabel') }}</span>
          <span class="mt-1 flex items-center gap-2">
            <input
              v-model="amount"
              type="text"
              inputmode="decimal"
              autocomplete="off"
              class="field num h-9 flex-1"
              :placeholder="usdExact(maxAmount)"
              :aria-invalid="error ? 'true' : undefined"
              :disabled="busy || !!pending"
              data-testid="earnings-amount"
            />
            <button type="button" class="btn btn-line btn-sm" :disabled="busy || !!pending" data-testid="earnings-all" @click="fillAll">{{ t('earnings.all') }}</button>
          </span>
        </label>
        <p class="mt-2 text-xs text-ink-3">
          {{ t('earnings.dialogHint', { available: usdExact(earnings.available), max: usdExact(maxAmount), min: usdExact(earnings.min_convert) }) }}
        </p>
        <p v-if="pending" class="mt-2 text-xs text-warn" data-testid="earnings-pending">{{ t('earnings.pendingHint', { amount: usdExact(pending.amount) }) }}</p>
        <p v-if="error" class="mt-2 text-sm text-seal" role="alert" data-testid="earnings-convert-error">{{ error }}</p>
      </form>
    </ConfirmDialog>
  </section>
</template>
