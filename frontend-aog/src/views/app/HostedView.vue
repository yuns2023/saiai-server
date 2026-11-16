<script setup lang="ts">
/**
 * 我的托管号: upstream accounts that belong to the signed-in user. Each one
 * shows its limit windows and a short self / other-users summary; picking one
 * lists its calls. Other users' calls carry usage only — never their key name
 * or what they were charged.
 */
import { computed, onMounted, onUnmounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { hostedApi, type HostedUsageSummary, type HostedWindow, type OwnedAccount, type OwnedUsageItem } from '@/api'
import type { ApiError } from '@/api/client'
import { intlLocale } from '@/i18n'
import { cost, count, dateTime, daysAgo, shortTime, ymd } from '@/lib/format'
import { localMidnight, MAX_SPAN_DAYS, rangeProblem, utcWindow } from '@/lib/hosted'
import { useHostedStore } from '@/stores/hosted'

type Range = 'today' | '7d' | '30d' | 'custom'
type UsageError = 'notFound' | 'badRequest' | 'failed'

const PAGE_SIZE = 50
/** At or above this utilization the bar turns amber. */
const WARN_AT = 80

const { t, te } = useI18n()
const hosted = useHostedStore()

/* ---------- Accounts ---------- */
const loading = ref(true)
const listFailed = ref(false)
const selectedId = ref<number | null>(null)

const sections = computed(() =>
  [
    { id: 'current', title: t('hosted.current'), lead: '', items: hosted.accounts.filter((a) => a.current) },
    { id: 'history', title: t('hosted.history'), lead: t('hosted.historyLead'), items: hosted.accounts.filter((a) => !a.current) }
  ].filter((s) => s.items.length)
)
const selected = computed(() => hosted.accounts.find((a) => a.account_id === selectedId.value) ?? null)

async function loadAccounts(): Promise<void> {
  loading.value = true
  listFailed.value = false
  try {
    await hosted.load()
  } catch {
    listFailed.value = true
  } finally {
    loading.value = false
  }
}

// Keep a valid selection: the first current account, else the first past one.
watch(
  () => hosted.accounts,
  (list) => {
    if (!list.some((a) => a.account_id === selectedId.value)) selectedId.value = (list.find((a) => a.current) ?? list[0])?.account_id ?? null
  },
  { immediate: true }
)

function statusLabel(s: string): string {
  return te(`hosted.status_${s}`) ? t(`hosted.status_${s}`) : s
}

function statusTag(s: string): string {
  if (s === 'available') return 'tag-ok'
  if (s === 'rate_limited' || s === 'temporarily_unavailable') return 'tag-warn'
  if (s === 'needs_attention') return 'tag-bad'
  return 'tag-off'
}

const PLATFORM_NAMES: Record<string, string> = { anthropic: 'Claude', openai: 'OpenAI', gemini: 'Gemini' }
const TYPE_NAMES: Record<string, string> = { oauth: 'OAuth', 'setup-token': 'Setup Token', apikey: 'API Key', upstream: 'Upstream', bedrock: 'Bedrock' }

function kindLabel(a: OwnedAccount): string {
  const platform = a.platform ? PLATFORM_NAMES[a.platform] ?? a.platform : ''
  const type = a.type ? TYPE_NAMES[a.type] ?? a.type : ''
  return [platform, type].filter(Boolean).join(' · ')
}

function windowLabel(key: string): string {
  return te(`hosted.window_${key}`) ? t(`hosted.window_${key}`) : key
}

function known(u: number | null): u is number {
  return u != null && Number.isFinite(u)
}

function pct(u: number | null): string {
  return known(u) ? `${Math.round(u)}%` : '—'
}

function fillWidth(w: HostedWindow): string {
  const u = w.utilization ?? 0
  return `${Math.min(100, Math.max(0, Number.isFinite(u) ? u : 0))}%`
}

function meterTone(w: HostedWindow): { track: string; fill: string } {
  if (w.expired) return { track: 'bg-rule-soft', fill: 'bg-ink-3' }
  if ((w.utilization ?? 0) >= WARN_AT) return { track: 'bg-warn-wash', fill: 'bg-warn' }
  return { track: 'bg-blue-wash', fill: 'bg-blue' }
}

function periods(a: OwnedAccount): Array<{ key: string; label: string; s: HostedUsageSummary | null }> {
  return [
    { key: '24h', label: t('hosted.last24h'), s: a.last_24h },
    { key: '7d', label: t('hosted.last7d'), s: a.last_7d }
  ]
}

function requests(n: number | null | undefined): string {
  return n == null ? '—' : t('hosted.requests', { n: count(n, intlLocale()) })
}

/* ---------- Usage of the selected account ---------- */
const range = ref<Range>('7d')
const from = ref(ymd(daysAgo(6)))
const to = ref(ymd(new Date()))
const rows = ref<OwnedUsageItem[]>([])
const nextCursor = ref<string | null>(null)
const usageLoading = ref(false)
const usageError = ref<UsageError | null>(null)
/** A failed "load more": the rows already shown stay. */
const moreError = ref<UsageError | null>(null)
let controller: AbortController | null = null
/** Bumped whenever the account or range changes; older answers are dropped. */
let usageGen = 0

const ranges = computed(() => [
  { id: 'today' as const, label: t('usage.today') },
  { id: '7d' as const, label: t('usage.last7') },
  { id: '30d' as const, label: t('usage.last30') },
  { id: 'custom' as const, label: t('usage.custom') }
])

const problem = computed(() => rangeProblem(from.value, to.value))
const problemText = computed(() => {
  if (problem.value === 'invalid') return t('hosted.rangeInvalid')
  if (problem.value === 'inverted') return t('hosted.rangeInverted')
  if (problem.value === 'tooLong') return t('hosted.rangeTooLong', { n: MAX_SPAN_DAYS })
  return ''
})
/** Native picker bounds that keep the span within MAX_SPAN_DAYS. */
const fromMin = computed(() => {
  const last = localMidnight(to.value)
  return last ? ymd(daysAgo(MAX_SPAN_DAYS - 1, last)) : undefined
})
const toMax = computed(() => {
  const first = localMidnight(from.value)
  return first ? ymd(daysAgo(-(MAX_SPAN_DAYS - 1), first)) : undefined
})

function errorText(kind: UsageError | null): string {
  if (kind === 'notFound') return t('hosted.notFound')
  if (kind === 'badRequest') return t('hosted.badRequest')
  return t('common.loadFailed')
}

function applyRange(r: Range): void {
  range.value = r
  const today = new Date()
  if (r === 'today') from.value = ymd(today)
  if (r === '7d') from.value = ymd(daysAgo(6))
  if (r === '30d') from.value = ymd(daysAgo(29))
  if (r !== 'custom') to.value = ymd(today)
}

function classify(e: unknown): UsageError {
  const status = (e as Partial<ApiError> | undefined)?.status
  if (status === 404) return 'notFound'
  if (status === 400) return 'badRequest'
  return 'failed'
}

/** First page for the current account + range, or the next page when `more`. */
async function loadUsage(more = false): Promise<void> {
  if (!more) {
    usageGen++
    controller?.abort()
    rows.value = []
    nextCursor.value = null
    usageError.value = null
  }
  moreError.value = null
  const id = selectedId.value
  const win = utcWindow(from.value, to.value)
  const cursor = more ? nextCursor.value : null
  if (id == null || !win || (more && !cursor)) {
    usageLoading.value = false
    return
  }
  const gen = usageGen
  controller = new AbortController()
  usageLoading.value = true
  try {
    const page = await hostedApi.listOwnedAccountUsage(id, { start: win.start, end: win.end, limit: PAGE_SIZE, cursor }, controller.signal)
    if (gen !== usageGen) return
    rows.value = more ? [...rows.value, ...page.items] : page.items
    nextCursor.value = page.next_cursor
  } catch (e) {
    if (gen !== usageGen || (e as { code?: string })?.code === 'ERR_CANCELED') return
    const kind = classify(e)
    if (!more) usageError.value = kind
    else {
      moreError.value = kind
      // No access any more: stop paging. A rejected cursor is retried from page one, other failures with the same cursor.
      if (kind === 'notFound') nextCursor.value = null
    }
  } finally {
    if (gen === usageGen) usageLoading.value = false
  }
}

watch([selectedId, from, to], () => loadUsage(), { immediate: true })

onMounted(loadAccounts)
onUnmounted(() => {
  usageGen++
  controller?.abort()
})
</script>

<template>
  <section aria-labelledby="hosted-title">
    <h1 id="hosted-title" class="text-lg font-semibold tracking-tight">{{ t('hosted.title') }}</h1>
    <p class="mt-1 max-w-[70ch] text-sm text-ink-2" data-testid="hosted-note">{{ t('hosted.lead') }}</p>

    <div v-if="loading && !hosted.accounts.length" class="mt-6 space-y-3" aria-hidden="true">
      <div class="skeleton h-24" />
      <div class="skeleton h-24" />
    </div>
    <p v-else-if="listFailed && !hosted.accounts.length" class="mt-6 text-sm text-seal" role="alert" data-testid="hosted-list-error">
      {{ t('common.loadFailed') }} <button type="button" class="btn btn-quiet btn-sm" @click="loadAccounts">{{ t('common.retry') }}</button>
    </p>
    <p v-else-if="!hosted.accounts.length" class="mt-6 border-t border-rule-strong py-10 text-center text-sm text-ink-2" data-testid="hosted-empty">
      {{ t('hosted.empty') }}
    </p>

    <template v-else>
      <p v-if="listFailed" class="mt-4 text-sm text-seal" role="alert">
        {{ t('common.loadFailed') }} <button type="button" class="btn btn-quiet btn-sm" @click="loadAccounts">{{ t('common.retry') }}</button>
      </p>

      <!-- Accounts: current first, then past ones (history only); one choice across both -->
      <div role="radiogroup" :aria-label="t('hosted.pick')">
        <section
          v-for="sec in sections"
          :key="sec.id"
          class="mt-8"
          :aria-labelledby="`hosted-${sec.id}-title`"
          :data-testid="`hosted-section-${sec.id}`"
        >
          <h2 :id="`hosted-${sec.id}-title`" class="text-md font-semibold">{{ sec.title }}</h2>
          <p v-if="sec.lead" class="mt-1 text-xs text-ink-3">{{ sec.lead }}</p>
          <ul class="mt-3 border-t border-rule-strong">
            <li
              v-for="a in sec.items"
              :key="a.account_id"
              class="border-b border-rule-soft transition-colors duration-150"
              :class="selectedId === a.account_id ? 'bg-blue-wash/60' : 'hover:bg-rule-soft/60'"
              :data-testid="`hosted-account-${a.account_id}`"
            >
              <label class="flex cursor-pointer items-start gap-3 px-3 py-4">
                <input
                  v-model="selectedId"
                  type="radio"
                  name="hosted-account"
                  :value="a.account_id"
                  class="mt-1.5 accent-[var(--blue)]"
                  :aria-labelledby="`hosted-name-${a.account_id}`"
                />
                <span class="grid min-w-0 flex-1 gap-x-10 gap-y-4 md:grid-cols-[minmax(0,1fr)_minmax(0,1fr)]">
                  <!-- Name, kind, status and the self / others summary -->
                  <span class="block min-w-0">
                    <span class="flex flex-wrap items-center gap-x-2 gap-y-1">
                      <span :id="`hosted-name-${a.account_id}`" class="min-w-0 truncate font-medium">{{ a.display_name }}</span>
                      <span v-if="a.status" class="tag" :class="statusTag(a.status)" data-testid="hosted-status">{{ statusLabel(a.status) }}</span>
                    </span>
                    <span v-if="kindLabel(a)" class="mt-0.5 block text-xs text-ink-3">{{ kindLabel(a) }}</span>
                    <span class="mt-3 grid grid-cols-[auto_minmax(0,1fr)_minmax(0,1fr)] gap-x-4 gap-y-1 text-sm" data-testid="hosted-stats">
                      <span />
                      <span class="label">{{ t('hosted.self') }}</span>
                      <span class="label">{{ t('hosted.others') }}</span>
                      <template v-for="p in periods(a)" :key="p.key">
                        <span class="whitespace-nowrap text-ink-2">{{ p.label }}</span>
                        <span class="num whitespace-nowrap" data-testid="hosted-self">
                        {{ requests(p.s?.self_requests) }} · {{ cost(p.s?.self_standard_cost) }}
                        <span v-if="p.s?.self_service_fee != null" class="block text-xs text-ink-3" data-testid="hosted-self-fee">
                          {{ t('hosted.serviceFee', { amount: cost(p.s.self_service_fee) }) }}
                        </span>
                      </span>
                        <span class="num whitespace-nowrap">{{ requests(p.s?.others_requests) }} · {{ cost(p.s?.others_standard_cost) }}</span>
                      </template>
                    </span>
                  </span>

                  <!-- Limit windows (current accounts only) -->
                  <span v-if="a.current" class="block min-w-0 space-y-3" data-testid="hosted-windows">
                    <span v-for="w in a.windows" :key="w.key" class="block" :data-testid="`hosted-window-${w.key}`">
                      <span class="flex items-baseline justify-between gap-3 text-sm">
                        <span class="text-ink-2">{{ windowLabel(w.key) }}</span>
                        <span class="num">{{ pct(w.utilization) }}</span>
                      </span>
                      <span
                        v-if="known(w.utilization)"
                        class="mt-1 block h-2 overflow-hidden rounded-full"
                        :class="meterTone(w).track"
                        role="meter"
                        aria-valuemin="0"
                        aria-valuemax="100"
                        :aria-valuenow="Math.min(100, Math.max(0, w.utilization))"
                        :aria-valuetext="pct(w.utilization)"
                        :aria-label="windowLabel(w.key)"
                      >
                        <span class="block h-full rounded-full" :class="meterTone(w).fill" :style="{ width: fillWidth(w) }" />
                      </span>
                      <span v-else class="mt-1 block h-2 rounded-full bg-rule-soft" aria-hidden="true" />
                      <span class="mt-0.5 block text-xs text-ink-3">
                        <template v-if="w.expired">{{ t('hosted.windowExpired') }}</template>
                        <span v-else-if="w.resets_at" :title="dateTime(w.resets_at, intlLocale())">{{ t('hosted.resetsAt', { time: shortTime(w.resets_at, intlLocale()) }) }}</span>
                      </span>
                    </span>
                    <span v-if="!a.windows.length" class="block text-sm text-ink-3">{{ t('hosted.noWindows') }}</span>
                    <span v-if="a.updated_at || a.stale" class="flex flex-wrap items-center gap-x-3 gap-y-1 text-xs text-ink-3">
                      <span v-if="a.updated_at" :title="dateTime(a.updated_at, intlLocale())">{{ t('hosted.sampledAt', { time: shortTime(a.updated_at, intlLocale()) }) }}</span>
                      <span v-if="a.stale" class="tag tag-warn" data-testid="hosted-stale">{{ t('hosted.stale') }}</span>
                    </span>
                  </span>
                </span>
              </label>
            </li>
          </ul>
        </section>
      </div>

      <!-- Calls on the selected account -->
      <section v-if="selected" class="mt-12" aria-labelledby="hosted-usage-title" data-testid="hosted-usage">
        <h2 id="hosted-usage-title" class="text-md font-semibold">{{ t('hosted.usageTitle', { name: selected.display_name }) }}</h2>
        <p class="mt-1 text-xs text-ink-3">{{ t('hosted.usageLead') }}</p>

        <div class="mt-4 flex flex-wrap items-end gap-x-6 gap-y-3">
          <div role="group" :aria-label="t('usage.range')" class="flex rounded border border-rule p-0.5">
            <button
              v-for="r in ranges"
              :key="r.id"
              type="button"
              class="h-8 rounded-[3px] px-3 text-sm transition-colors duration-150"
              :class="range === r.id ? 'bg-blue text-on-blue' : 'text-ink-2 hover:text-ink'"
              :aria-pressed="range === r.id"
              :data-testid="`hosted-range-${r.id}`"
              @click="applyRange(r.id)"
            >
              {{ r.label }}
            </button>
          </div>
          <div v-if="range === 'custom'" class="flex items-end gap-2">
            <label class="block">
              <span class="label">{{ t('usage.from') }}</span>
              <input
                v-model="from"
                type="date"
                class="field num mt-1 h-9 w-[10.5rem]"
                :min="fromMin"
                :max="to"
                :aria-invalid="problem ? 'true' : undefined"
                data-testid="hosted-from"
              />
            </label>
            <label class="block">
              <span class="label">{{ t('usage.to') }}</span>
              <input
                v-model="to"
                type="date"
                class="field num mt-1 h-9 w-[10.5rem]"
                :min="from"
                :max="toMax"
                :aria-invalid="problem ? 'true' : undefined"
                data-testid="hosted-to"
              />
            </label>
          </div>
        </div>
        <p v-if="problemText" class="mt-2 text-sm text-seal" role="alert" data-testid="hosted-range-problem">{{ problemText }}</p>

        <div v-if="!problem" class="mt-4">
          <p v-if="usageError" class="py-6 text-sm text-seal" role="alert" data-testid="hosted-usage-error">
            {{ errorText(usageError) }}
            <button v-if="usageError === 'failed'" type="button" class="btn btn-quiet btn-sm" @click="loadUsage()">{{ t('common.retry') }}</button>
          </p>
          <p v-else-if="!usageLoading && !rows.length" class="border-b border-rule-soft py-10 text-center text-sm text-ink-2" data-testid="hosted-usage-empty">
            {{ t('hosted.noUsage') }}
          </p>
          <template v-else>
            <div class="relative overflow-x-auto">
              <table class="ledger min-w-[52rem]" data-testid="hosted-usage-table">
                <thead>
                  <tr>
                    <th class="w-[7.5rem]">{{ t('hosted.time') }}</th>
                    <th>{{ t('hosted.model') }}</th>
                    <th class="amount">{{ t('hosted.input') }}</th>
                    <th class="amount">{{ t('hosted.output') }}</th>
                    <th class="amount">{{ t('hosted.cacheRead') }}</th>
                    <th class="amount">{{ t('hosted.cacheWrite') }}</th>
                    <th class="amount">{{ t('hosted.standard') }}</th>
                    <th>{{ t('hosted.caller') }}</th>
                    <th class="amount">{{ t('hosted.charged') }}</th>
                  </tr>
                </thead>
                <tbody v-if="usageLoading && !rows.length">
                  <tr v-for="i in 5" :key="i">
                    <td colspan="9"><div class="skeleton h-4 w-full" /></td>
                  </tr>
                </tbody>
                <tbody v-else>
                  <tr v-for="(r, i) in rows" :key="`${i}-${r.created_at}`" data-testid="hosted-usage-row">
                    <td class="num whitespace-nowrap text-ink-2" :title="dateTime(r.created_at, intlLocale())">{{ shortTime(r.created_at, intlLocale()) }}</td>
                    <td class="max-w-[9rem] truncate font-mono text-xs leading-5 sm:max-w-[14rem]" :title="r.model">{{ r.model }}</td>
                    <td class="amount num text-ink-2">{{ count(r.input_tokens, intlLocale()) }}</td>
                    <td class="amount num text-ink-2">{{ count(r.output_tokens, intlLocale()) }}</td>
                    <td class="amount num text-ink-2">{{ count(r.cache_read_tokens, intlLocale()) }}</td>
                    <td class="amount num text-ink-2">{{ count(r.cache_creation_tokens, intlLocale()) }}</td>
                    <td class="amount num" data-testid="hosted-standard">{{ cost(r.standard_cost) }}</td>
                    <td class="max-w-[12rem] truncate" data-testid="hosted-caller">
                      <template v-if="r.is_self">
                        {{ t('hosted.callerSelf') }}<span v-if="r.api_key_name" class="text-ink-3"> · {{ r.api_key_name }}</span>
                      </template>
                      <span v-else class="text-ink-2">{{ t('hosted.callerOthers') }}</span>
                    </td>
                    <td class="amount num font-medium" data-testid="hosted-charged">{{ r.is_self ? cost(r.charged) : '—' }}</td>
                  </tr>
                </tbody>
              </table>
            </div>
            <div v-if="nextCursor || moreError" class="flex items-center justify-center gap-3 pt-4">
              <p v-if="moreError" class="text-sm text-seal" role="alert" data-testid="hosted-more-error">{{ errorText(moreError) }}</p>
              <button v-if="moreError === 'badRequest'" type="button" class="btn btn-line btn-sm" data-testid="hosted-reload" @click="loadUsage()">
                {{ t('hosted.reload') }}
              </button>
              <button
                v-else-if="nextCursor"
                type="button"
                class="btn btn-line btn-sm"
                :disabled="usageLoading"
                data-testid="hosted-load-more"
                @click="loadUsage(true)"
              >
                {{ usageLoading ? t('hosted.loadingMore') : moreError ? t('common.retry') : t('hosted.loadMore') }}
              </button>
            </div>
          </template>
        </div>
      </section>
    </template>
  </section>
</template>
