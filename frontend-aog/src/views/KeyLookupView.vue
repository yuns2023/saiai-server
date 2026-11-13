<script setup lang="ts">
/** Check a key's balance and usage without signing in (GET /v1/usage with the key). */
import { computed, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import PrefsMenu from '@/components/PrefsMenu.vue'
import Icon from '@/components/Icon.vue'
import { useAppStore } from '@/stores/app'
import { intlLocale } from '@/i18n'
import { compact, cost, count, dateOnly, daysAgo, money, shortTime, ymd } from '@/lib/format'

interface Totals {
  requests?: number
  total_tokens?: number
  actual_cost?: number
  cost?: number
}
interface LookupResult {
  billing_mode?: 'subscription' | 'balance'
  billing?: { type: 'wallet' | 'subscription'; balance?: number; balance_visible?: boolean; available?: boolean; plan_name?: string }
  isValid: boolean
  status: string
  mode: 'quota_limited' | 'unrestricted'
  balance?: number
  remaining?: number
  planName?: string
  quota?: { limit: number; used: number; remaining: number }
  expires_at?: string | null
  usage?: { today?: Totals; total?: Totals; range?: Totals }
  recent_usage?: {
    records: Array<{ created_at: string; model: string; total_tokens: number; actual_cost: number }>
    pagination: { total: number; page: number; page_size: number; pages: number }
  }
}

const { t } = useI18n()
const app = useAppStore()
const key = ref('')
const show = ref(false)
const range = ref<'today' | '7d' | '30d'>('7d')
const busy = ref(false)
const error = ref('')
const result = ref<LookupResult | null>(null)

/** Only the requested period; never substitute another period's numbers. */
/** The range the shown result was fetched for (not the current selector). */
const resultRange = ref<'today' | '7d' | '30d'>('7d')
let lookupSeq = 0
// Editing the key or the range invalidates what is on screen.
watch([key, range], () => {
  lookupSeq++
  result.value = null
  error.value = ''
  busy.value = false
})

/** Only the requested period; never substitute another period's numbers. */
const rangeTotals = computed(() => (resultRange.value === 'today' ? result.value?.usage?.range ?? result.value?.usage?.today : result.value?.usage?.range) ?? null)

/** Status comes from the machine status, not isValid (which is "queryable"). */
const statusView = computed(() => {
  const s = result.value?.status ?? ''
  if (s === 'active') return { cls: 'tag-ok', text: t('keys.status_active') }
  if (s === 'quota_exhausted') return { cls: 'tag-warn', text: t('keys.status_quota_exhausted') }
  if (s === 'expired') return { cls: 'tag-bad', text: t('keys.status_expired') }
  if (s === 'inactive' || s === 'disabled') return { cls: 'tag-off', text: t('keys.status_inactive') }
  return { cls: 'tag-off', text: s || '—' }
})

/** Which pot the remaining number belongs to. */
const remainingView = computed(() => {
  const r = result.value
  if (!r) return null
  if (r.quota) return { label: t('lookup.quotaLeft'), value: r.quota.remaining }
  const subscription = r.billing_mode === 'subscription' || r.billing?.type === 'subscription'
  if (subscription) return { label: t('lookup.subscriptionLeft'), value: r.remaining ?? null }
  const wallet = r.billing?.balance_visible === false ? null : r.billing?.balance ?? r.balance ?? r.remaining
  return { label: t('lookup.balance'), value: wallet ?? null }
})

async function lookup(): Promise<void> {
  const k = key.value.trim()
  if (!k || busy.value) return
  busy.value = true
  error.value = ''
  const seq = ++lookupSeq
  const asked = range.value
  const end = new Date()
  const start = asked === 'today' ? end : daysAgo(asked === '7d' ? 6 : 29)
  const params = new URLSearchParams({ start_date: ymd(start), end_date: ymd(end), records_page: '1', records_page_size: '10' })
  try {
    const res = await fetch(`/v1/usage?${params}`, {
      headers: { Authorization: `Bearer ${k}` },
      cache: 'no-store',
      referrerPolicy: 'no-referrer'
    })
    const body = await res.json().catch(() => null)
    if (!res.ok) {
      throw new Error(body?.error?.message || body?.message || t('lookup.failed', { status: res.status }))
    }
    if (seq !== lookupSeq) return
    resultRange.value = asked
    result.value = body as LookupResult
  } catch (e) {
    if (seq !== lookupSeq) return
    result.value = null
    error.value = (e as Error).message || t('lookup.failedGeneric')
  } finally {
    if (seq === lookupSeq) busy.value = false
  }
}
</script>

<template>
  <div class="min-h-dvh bg-paper">
    <header class="flex h-14 items-center justify-between px-5 sm:px-10">
      <RouterLink to="/" class="text-md font-semibold tracking-tight text-ink no-underline">{{ app.siteName }}</RouterLink>
      <PrefsMenu compact />
    </header>
    <main class="mx-auto max-w-3xl px-5 pb-16 pt-10 sm:px-10">
      <h1 class="rule-double pb-3 text-xl font-semibold tracking-tight">{{ t('lookup.title') }}</h1>
      <p class="mt-3 text-sm text-ink-2">{{ t('lookup.lead') }}</p>

      <form class="mt-6 flex flex-wrap items-end gap-3" novalidate @submit.prevent="lookup">
        <label class="block min-w-[16rem] flex-1">
          <span class="label">API Key</span>
          <span class="relative mt-1 block">
            <input
              v-model="key"
              class="field pr-10 font-mono text-xs"
              :type="show ? 'text' : 'password'"
              autocomplete="off"
              spellcheck="false"
              placeholder="sk-…"
            />
            <button type="button" class="absolute inset-y-0 right-0 px-3 text-ink-3 hover:text-ink" :aria-label="show ? t('keys.hide') : t('keys.show')" @click="show = !show">
              <Icon :name="show ? 'eyeOff' : 'eye'" :size="16" />
            </button>
          </span>
        </label>
        <label class="block">
          <span class="label">{{ t('usage.range') }}</span>
          <select v-model="range" class="field mt-1 w-32">
            <option value="today">{{ t('usage.today') }}</option>
            <option value="7d">{{ t('usage.last7') }}</option>
            <option value="30d">{{ t('usage.last30') }}</option>
          </select>
        </label>
        <button type="submit" class="btn btn-primary h-10" :disabled="busy || !key.trim()">{{ busy ? t('lookup.checking') : t('lookup.check') }}</button>
      </form>
      <p class="mt-2 text-xs text-ink-3">{{ t('lookup.privacy') }}</p>
      <p v-if="error" class="mt-4 text-sm text-seal" role="alert">{{ error }}</p>

      <section v-if="result" class="mt-8" aria-live="polite">
        <dl class="rule-double grid grid-cols-2 gap-x-8 gap-y-4 border-t border-rule-strong py-4 sm:grid-cols-4">
          <div>
            <dt class="label">{{ t('lookup.status') }}</dt>
            <dd class="mt-0.5"><span class="tag" :class="statusView.cls">{{ statusView.text }}</span></dd>
          </div>
          <div>
            <dt class="label">{{ remainingView?.label }}</dt>
            <dd class="num mt-0.5 text-md font-semibold">{{ money(remainingView?.value) }}</dd>
          </div>
          <div>
            <dt class="label">{{ t('lookup.rangeSpend') }}</dt>
            <dd class="num mt-0.5 text-md">{{ money(rangeTotals?.actual_cost ?? rangeTotals?.cost, 4) }}</dd>
          </div>
          <div>
            <dt class="label">{{ t('lookup.rangeRequests') }}</dt>
            <dd class="num mt-0.5 text-md">{{ count(rangeTotals?.requests, intlLocale()) }}</dd>
          </div>
        </dl>
        <p v-if="result.planName || result.expires_at" class="mt-3 text-sm text-ink-2">
          <template v-if="result.planName">{{ result.planName }}</template>
          <template v-if="result.expires_at"> · {{ t('keys.expires') }} {{ dateOnly(result.expires_at, intlLocale()) }}</template>
        </p>

        <h2 class="mt-8 text-md font-semibold">{{ t('overview.recent') }}</h2>
        <p v-if="!result.recent_usage?.records?.length" class="mt-2 border-t border-rule-strong py-4 text-sm text-ink-3">{{ t('usage.empty') }}</p>
        <table v-else class="ledger mt-2">
          <thead>
            <tr>
              <th>{{ t('charge.time') }}</th>
              <th>{{ t('charge.model') }}</th>
              <th class="amount">{{ t('charge.tokens') }}</th>
              <th class="amount">{{ t('charge.amount') }}</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="(r, i) in result.recent_usage.records" :key="i">
              <td class="num whitespace-nowrap text-ink-2">{{ shortTime(r.created_at, intlLocale()) }}</td>
              <td class="max-w-[14rem] truncate font-mono text-xs leading-5">{{ r.model }}</td>
              <td class="amount num text-ink-2">{{ compact(r.total_tokens, intlLocale()) }}</td>
              <td class="amount num">{{ cost(r.actual_cost) }}</td>
            </tr>
          </tbody>
        </table>
      </section>
    </main>
  </div>
</template>
