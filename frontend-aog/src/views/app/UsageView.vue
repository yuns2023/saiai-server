<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import ChargeTable from '@/components/ChargeTable.vue'
import Pager from '@/components/Pager.vue'
import { keysApi, usageApi, type ModelTotals } from '@/api'
import { intlLocale } from '@/i18n'
import { compact, count, daysAgo, money, ymd } from '@/lib/format'
import type { ApiKey, UsageLog, UsageStatsResponse } from '@/types'

type Range = 'today' | '7d' | '30d' | 'custom'

const { t } = useI18n()
const range = ref<Range>('7d')
const start = ref(ymd(daysAgo(6)))
const end = ref(ymd(new Date()))
const keyId = ref<number | ''>('')
const model = ref('')
const page = ref(1)
const pageSize = 20

const keys = ref<ApiKey[]>([])
const models = ref<ModelTotals[]>([])
const modelsFailed = ref(false)
const rows = ref<UsageLog[]>([])
const total = ref(0)
const pages = ref(1)
const stats = ref<UsageStatsResponse | null>(null)
const loading = ref(true)
const failed = ref(false)
let controller: AbortController | null = null

/**
 * Every response is tied to the filter generation it was requested for; a
 * late answer for an older range/key/model is dropped instead of mixing its
 * totals with newer lines.
 */
let filterGen = 0
let listGen = 0
/** Model totals depend on range + key only, so model changes do not void them. */
let modelsGen = 0

const ranges = computed(() => [
  { id: 'today' as const, label: t('usage.today') },
  { id: '7d' as const, label: t('usage.last7') },
  { id: '30d' as const, label: t('usage.last30') },
  { id: 'custom' as const, label: t('usage.custom') }
])

/**
 * Closing totals follow the same filters as the lines: /usage/stats has no
 * model filter, so with a model selected the totals come from that model's
 * row of the per-model breakdown for the same range and key.
 */
const totals = computed(() => {
  if (model.value) {
    const m = models.value.find((x) => x.model === model.value)
    return m ? { requests: m.requests, tokens: m.total_tokens, standard: m.cost, charged: m.actual_cost } : null
  }
  const st = stats.value
  return st ? { requests: st.total_requests, tokens: st.total_tokens, standard: st.total_cost, charged: st.total_actual_cost } : null
})

function applyRange(r: Range): void {
  range.value = r
  const today = new Date()
  if (r === 'today') start.value = ymd(today)
  if (r === '7d') start.value = ymd(daysAgo(6))
  if (r === '30d') start.value = ymd(daysAgo(29))
  if (r !== 'custom') end.value = ymd(today)
}

function baseFilters() {
  return { start_date: start.value, end_date: end.value, api_key_id: keyId.value === '' ? undefined : keyId.value }
}

/** Lines (+ totals on a fresh filter). `withTotals` is false only when paging. */
async function load(withTotals = true): Promise<void> {
  controller?.abort()
  controller = new AbortController()
  const fGen = filterGen
  const lGen = ++listGen
  loading.value = true
  failed.value = false
  if (withTotals) stats.value = null
  const filters = baseFilters()
  try {
    const [list, s] = await Promise.all([
      usageApi.query({ ...filters, model: model.value || undefined, page: page.value, page_size: pageSize }, controller.signal),
      withTotals ? usageApi.stats(filters) : Promise.resolve(stats.value)
    ])
    if (fGen !== filterGen || lGen !== listGen) return
    rows.value = list.items
    total.value = list.total
    pages.value = list.pages
    stats.value = s
    loading.value = false
  } catch (e) {
    if (fGen !== filterGen || lGen !== listGen) return
    if ((e as { code?: string }).code === 'ERR_CANCELED') return
    failed.value = true
    loading.value = false
  }
}

async function loadModels(): Promise<void> {
  const gen = ++modelsGen
  models.value = []
  modelsFailed.value = false
  try {
    const list = await usageApi.models(baseFilters())
    if (gen !== modelsGen) return
    models.value = list
  } catch {
    if (gen === modelsGen) modelsFailed.value = true
  }
}

function refreshAll(): void {
  filterGen++
  page.value = 1
  loadModels()
  load()
}

watch([start, end, keyId], refreshAll)
watch(model, () => {
  filterGen++
  page.value = 1
  load()
})

onMounted(() => {
  keysApi.list(1, 100).then((r) => (keys.value = r.items)).catch(() => undefined)
  refreshAll()
})

function go(p: number): void {
  page.value = p
  load(false)
}
</script>

<template>
  <section aria-labelledby="usage-title">
    <h1 id="usage-title" class="text-lg font-semibold tracking-tight">{{ t('usage.title') }}</h1>
    <p class="mt-1 text-sm text-ink-2">{{ t('usage.lead') }}</p>

    <!-- Filters -->
    <div class="mt-6 flex flex-wrap items-end gap-x-6 gap-y-3">
      <div role="group" :aria-label="t('usage.range')" class="flex rounded border border-rule p-0.5">
        <button
          v-for="r in ranges"
          :key="r.id"
          type="button"
          class="h-8 rounded-[3px] px-3 text-sm transition-colors duration-150"
          :class="range === r.id ? 'bg-blue text-on-blue' : 'text-ink-2 hover:text-ink'"
          :aria-pressed="range === r.id"
          @click="applyRange(r.id)"
        >
          {{ r.label }}
        </button>
      </div>
      <div v-if="range === 'custom'" class="flex items-end gap-2">
        <label class="block">
          <span class="label">{{ t('usage.from') }}</span>
          <input v-model="start" type="date" class="field num mt-1 h-9 w-[10.5rem]" :max="end" />
        </label>
        <label class="block">
          <span class="label">{{ t('usage.to') }}</span>
          <input v-model="end" type="date" class="field num mt-1 h-9 w-[10.5rem]" :min="start" />
        </label>
      </div>
      <label class="block">
        <span class="label">{{ t('usage.key') }}</span>
        <select v-model="keyId" class="field mt-1 h-9 w-48">
          <option value="">{{ t('usage.allKeys') }}</option>
          <option v-for="k in keys" :key="k.id" :value="k.id">{{ k.name }}</option>
        </select>
      </label>
      <label class="block">
        <span class="label">{{ t('usage.model') }}</span>
        <select v-model="model" class="field mt-1 h-9 w-56 font-mono text-xs">
          <option value="">{{ t('usage.allModels') }}</option>
          <option v-if="model && !models.some((m) => m.model === model)" :value="model">{{ model }}</option>
          <option v-for="m in models" :key="m.model" :value="m.model">{{ m.model }}</option>
        </select>
      </label>
    </div>

    <!-- Totals: the statement's closing lines -->
    <dl class="rule-double mt-6 grid grid-cols-2 gap-x-8 gap-y-3 border-t border-rule-strong py-4 sm:grid-cols-4">
      <div>
        <dt class="label">{{ t('usage.requests') }}</dt>
        <dd class="num mt-0.5 text-md">{{ totals ? count(totals.requests, intlLocale()) : '—' }}</dd>
      </div>
      <div>
        <dt class="label">{{ t('usage.tokens') }}</dt>
        <dd class="num mt-0.5 text-md">{{ totals ? compact(totals.tokens, intlLocale()) : '—' }}</dd>
      </div>
      <div>
        <dt class="label">{{ t('usage.standardTotal') }}</dt>
        <dd class="num mt-0.5 text-md text-ink-2">{{ totals ? money(totals.standard, 4) : '—' }}</dd>
      </div>
      <div>
        <dt class="label">{{ t('usage.chargedTotal') }}</dt>
        <dd class="num mt-0.5 text-md font-semibold">{{ totals ? money(totals.charged, 4) : '—' }}</dd>
      </div>
    </dl>
    <p v-if="model && modelsFailed" class="mt-2 text-sm text-seal" role="alert">
      {{ t('usage.modelTotalsFailed') }} <button type="button" class="btn btn-quiet btn-sm" @click="loadModels">{{ t('common.retry') }}</button>
    </p>

    <div class="mt-6">
      <p v-if="failed" class="py-6 text-sm text-seal" role="alert">
        {{ t('common.loadFailed') }} <button type="button" class="btn btn-quiet btn-sm" @click="refreshAll">{{ t('common.retry') }}</button>
      </p>
      <p v-else-if="!loading && !rows.length" class="border-b border-rule-soft py-10 text-center text-sm text-ink-2">{{ t('usage.empty') }}</p>
      <template v-else>
        <ChargeTable :rows="rows" :loading="loading" :class="loading && rows.length && 'opacity-60 transition-opacity'" />
        <Pager :page="page" :pages="pages" :total="total" :busy="loading" @go="go" />
      </template>
    </div>
  </section>
</template>
