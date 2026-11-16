<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref, watch } from 'vue'
import { useRoute } from 'vue-router'
import { useI18n } from 'vue-i18n'
import QRCode from 'qrcode'
import CopyButton from '@/components/CopyButton.vue'
import Icon from '@/components/Icon.vue'
import SealStamp from '@/components/SealStamp.vue'
import {
  cryptoPayApi,
  explorerUrl,
  type CryptoDepositStatus,
  type CryptoOrder,
  type CryptoPayConfig
} from '@/api'
import { intlLocale } from '@/i18n'
import { compareDecimal, fromUnits, isPositive, toUnits, usd } from '@/lib/decimal'
import { dateOnly, dateTime } from '@/lib/format'
import { useAuthStore } from '@/stores/auth'

/** Polling cadence: live orders every 5 s; after expiry late transfers are rarer, so 30 s. */
const LIVE_POLL_MS = 5000
const EXPIRED_POLL_MS = 30000

const { t } = useI18n()
const route = useRoute()
const auth = useAuthStore()

const orderId = computed(() => String(route.params.id ?? ''))
const order = ref<CryptoOrder | null>(null)
const config = ref<CryptoPayConfig | null>(null)
const loading = ref(true)
const loadError = ref<'' | 'failed' | 'notFound'>('')
const qr = ref('')
const now = ref(Date.now())

const net = computed(() => config.value?.networks.find((n) => n.network === order.value?.network) ?? null)
const networkLabel = computed(() => net.value?.label || order.value?.network || '')
const expiresAt = computed(() => (order.value ? new Date(order.value.expires_at).getTime() : 0))
const isExpired = computed(() => !!order.value && (order.value.status === 'expired' || (order.value.status === 'pending' && now.value >= expiresAt.value)))
/** Past the deadline whatever the status (a paid order keeps polling, but slower once expired). */
const pastDeadline = computed(() => !!order.value && now.value >= expiresAt.value)
/** Only the server decides "paid" (finalized ≥ amount); received may still include unconfirmed inflows. */
const paidFull = computed(() => order.value?.status === 'paid')
const displayStatus = computed(() => (order.value?.status === 'pending' && isExpired.value ? 'expired' : order.value?.status ?? 'pending'))
const countdown = computed(() => {
  const left = Math.max(0, Math.floor((expiresAt.value - now.value) / 1000))
  const h = Math.floor(left / 3600)
  const m = Math.floor((left % 3600) / 60)
  const s = left % 60
  const pad = (n: number) => String(n).padStart(2, '0')
  return h > 0 ? `${h}:${pad(m)}:${pad(s)}` : `${pad(m)}:${pad(s)}`
})
const remaining = computed(() => {
  const o = order.value
  if (!o || o.status === 'paid') return null
  const due = toUnits(o.amount)
  const got = toUnits(o.received_amount)
  if (due === null || got === null || got >= due) return null
  return fromUnits(due - got)
})
const creditedFull = computed(() => !!order.value && isPositive(order.value.amount) && compareDecimal(order.value.credited_amount, order.value.amount) >= 0)
const lastCreditedAt = computed(() => order.value?.deposits.find((d) => d.credited_at)?.credited_at ?? null)
const hasHeld = computed(() => !!order.value?.deposits.some((d) => d.status === 'held'))
const hasOrphaned = computed(() => !!order.value?.deposits.some((d) => d.status === 'orphaned'))
const addressUrl = computed(() => (order.value ? explorerUrl(net.value?.explorer_address_url, 'addr', order.value.address) : ''))

function txUrl(hash: string): string {
  return explorerUrl(net.value?.explorer_tx_url, 'tx', hash)
}

function shortHash(hash: string): string {
  return hash.length > 16 ? `${hash.slice(0, 8)}…${hash.slice(-6)}` : hash
}

function depositTag(s: CryptoDepositStatus): string {
  if (s === 'credited') return 'tag-ok'
  if (s === 'confirming' || s === 'held') return 'tag-warn'
  return 'tag-off'
}

function orderTag(s: string): string {
  if (s === 'paid') return 'tag-ok'
  if (s === 'pending') return 'tag-warn'
  return 'tag-off'
}

/* ---------- Loading & polling ---------- */
let pollTimer: number | undefined
let clock: number | undefined
let inFlight = false
let stopped = false
/** Bumped on route change so a late answer for the previous order is ignored entirely. */
let generation = 0

async function refresh(): Promise<void> {
  if (inFlight || stopped) return
  inFlight = true
  const gen = generation
  const id = orderId.value
  const current = () => !stopped && gen === generation
  try {
    const latest = await cryptoPayApi.getOrder(id)
    if (current()) {
      const before = order.value?.id === latest.id ? order.value.credited_amount : null
      order.value = latest
      loadError.value = ''
      // Balance moved: refresh the header figure (best effort).
      if (before !== null && compareDecimal(latest.credited_amount, before) > 0) auth.refreshUser().catch(() => undefined)
    }
  } catch (e) {
    if (current() && !order.value) {
      const status = (e as { status?: number } | undefined)?.status
      loadError.value = status === 404 || status === 403 ? 'notFound' : 'failed'
    }
  } finally {
    inFlight = false
    if (current()) loading.value = false
  }
  // The route moved on while this request was out: load the new order now.
  if (!stopped && gen !== generation) await refresh()
}

function schedule(): void {
  window.clearTimeout(pollTimer)
  pollTimer = undefined
  if (stopped || document.hidden || loadError.value === 'notFound') return
  pollTimer = window.setTimeout(tick, pastDeadline.value || order.value?.status === 'expired' ? EXPIRED_POLL_MS : LIVE_POLL_MS)
}

async function tick(): Promise<void> {
  // A background tab makes no requests, except the first load so the page is not blank.
  if (!document.hidden || !order.value) await refresh()
  schedule()
}

function onVisibility(): void {
  if (document.hidden) {
    window.clearTimeout(pollTimer)
    pollTimer = undefined
  } else {
    now.value = Date.now()
    tick()
  }
}

watch(
  () => order.value?.address,
  async (address) => {
    qr.value = ''
    if (!address) return
    const image = await QRCode.toDataURL(address, { width: 280, margin: 1, color: { dark: '#14203a', light: '#ffffff' } }).catch(() => '')
    if (!stopped && order.value?.address === address) qr.value = image
  }
)

watch(orderId, () => {
  generation++
  order.value = null
  loading.value = true
  loadError.value = ''
  tick()
})

onMounted(() => {
  clock = window.setInterval(() => (now.value = Date.now()), 1000)
  document.addEventListener('visibilitychange', onVisibility)
  cryptoPayApi
    .config()
    .then((c) => {
      if (!stopped) config.value = c
    })
    .catch(() => undefined)
  tick()
})

onUnmounted(() => {
  stopped = true
  window.clearTimeout(pollTimer)
  window.clearInterval(clock)
  document.removeEventListener('visibilitychange', onVisibility)
})
</script>

<template>
  <section aria-labelledby="checkout-title">
    <RouterLink to="/billing" class="text-sm no-underline">{{ t('crypto.back') }}</RouterLink>
    <h1 id="checkout-title" class="mt-3 text-lg font-semibold tracking-tight">
      {{ t('crypto.checkoutTitle') }}
      <span v-if="order" class="num ml-1 text-sm font-normal text-ink-3">{{ t('crypto.order', { id: order.id }) }}</span>
    </h1>

    <div v-if="loading && !order" class="mt-6 space-y-3"><div class="skeleton h-16" /><div class="skeleton h-10" /><div class="skeleton h-10" /></div>

    <p v-else-if="!order" class="mt-6 text-sm text-seal" role="alert" data-testid="crypto-load-error">
      {{ loadError === 'notFound' ? t('crypto.notFound') : t('crypto.loadFailed') }}
      <button v-if="loadError !== 'notFound'" type="button" class="btn btn-quiet btn-sm" @click="tick">{{ t('common.retry') }}</button>
    </p>

    <template v-else>
      <!-- Receipt header -->
      <div class="rule-double mt-5 flex flex-wrap items-end justify-between gap-6 pb-5">
        <div>
          <div class="label">{{ t('crypto.payAmount') }}</div>
          <div class="num mt-1 text-figure font-semibold leading-none tracking-tight" data-testid="crypto-amount">{{ usd(order.amount) }}</div>
          <div class="mt-2 text-sm text-ink-2">{{ order.asset }} · {{ networkLabel }}</div>
        </div>
        <div class="flex items-end gap-5">
          <SealStamp v-if="creditedFull" class="hidden sm:block" :text="t('billing.seal')" :sub="dateOnly(lastCreditedAt, intlLocale())" />
          <div class="text-right">
            <span class="tag" :class="orderTag(displayStatus)" data-testid="crypto-status">{{ t(`crypto.orderStatus_${displayStatus}`) }}</span>
            <template v-if="!pastDeadline">
              <div class="label mt-2">{{ t('crypto.timeLeft') }}</div>
              <div class="num mt-0.5 text-lg font-semibold" data-testid="crypto-countdown" aria-live="off">{{ countdown }}</div>
            </template>
          </div>
        </div>
      </div>

      <p v-if="isExpired" class="mt-4 rounded border border-warn/40 bg-warn-wash px-3 py-2 text-sm text-warn" role="status" data-testid="crypto-expired">
        {{ t('crypto.expired') }}
      </p>

      <!-- Where to send -->
      <div class="mt-8 grid gap-10 lg:grid-cols-[minmax(0,1fr)_auto]">
        <section aria-labelledby="send-title" class="min-w-0">
          <h2 id="send-title" class="text-md font-semibold">
            {{ t('crypto.sendTo', { amount: usd(order.amount), asset: order.asset, network: networkLabel }) }}
          </h2>
          <div class="label mt-4">{{ t('crypto.address') }}</div>
          <div class="mt-1 flex flex-wrap items-start gap-2 sm:flex-nowrap">
            <code class="min-w-0 flex-1 break-all rounded border border-rule bg-shell px-3 py-2 font-mono text-sm leading-6 text-ink" data-testid="crypto-address">{{ order.address }}</code>
            <CopyButton :value="order.address" class="h-10 shrink-0" />
          </div>
          <a v-if="addressUrl" :href="addressUrl" target="_blank" rel="noopener noreferrer" class="mt-2 inline-flex items-center gap-1 text-sm">
            {{ t('crypto.viewAddress') }} <Icon name="external" :size="14" />
          </a>
          <div class="mt-5 flex justify-center lg:hidden">
            <img v-if="qr" :src="qr" :alt="t('crypto.qrAlt')" width="200" height="200" class="rounded border border-rule bg-white p-2" />
            <div v-else class="skeleton h-[200px] w-[200px]" />
          </div>

          <div class="mt-5 rounded border border-seal/40 bg-seal-wash px-3 py-2.5 text-sm" role="note" data-testid="crypto-warning">
            <p class="font-semibold text-seal">{{ t('crypto.warnTitle', { asset: order.asset, network: networkLabel }) }}</p>
            <p class="mt-0.5 text-ink">{{ t('crypto.warnBody') }}</p>
          </div>

          <h3 class="mt-6 text-sm font-medium">{{ t('crypto.noteTitle') }}</h3>
          <ul class="mt-2 list-disc space-y-1 pl-5 text-sm text-ink-2">
            <li>{{ t('crypto.notePartial') }}</li>
            <li>{{ t('crypto.noteChain') }}</li>
            <li>
              {{ net ? t('crypto.noteFinality', { network: networkLabel, n: net.finality_hint_minutes }) : t('crypto.noteFinalityGeneric') }}
            </li>
            <li>{{ t('crypto.noteLate') }}</li>
            <li>{{ t('crypto.noteAddress', { network: networkLabel }) }}</li>
          </ul>
        </section>

        <div class="hidden lg:block">
          <img v-if="qr" :src="qr" :alt="t('crypto.qrAlt')" width="200" height="200" class="rounded border border-rule bg-white p-2" data-testid="crypto-qr" />
          <div v-else class="skeleton h-[200px] w-[200px]" />
        </div>
      </div>

      <!-- Progress -->
      <section class="mt-12" aria-labelledby="progress-title">
        <h2 id="progress-title" class="text-md font-semibold">{{ t('crypto.progress') }}</h2>
        <dl class="mt-3 border-t border-rule-strong text-sm" data-testid="crypto-progress">
          <div class="flex items-baseline justify-between gap-4 border-b border-rule-soft py-2.5">
            <dt class="text-ink-2">{{ t('crypto.received') }}</dt>
            <dd class="num font-semibold" data-testid="crypto-received">{{ usd(order.received_amount) }}</dd>
          </div>
          <div class="flex items-baseline justify-between gap-4 border-b border-rule-soft py-2.5">
            <dt class="text-ink-2">{{ t('crypto.confirming') }}</dt>
            <dd class="num" :class="isPositive(order.confirming_amount) ? 'text-warn' : 'text-ink-3'" data-testid="crypto-confirming">{{ usd(order.confirming_amount) }}</dd>
          </div>
          <div class="flex items-baseline justify-between gap-4 border-b border-rule-soft py-2.5">
            <dt class="text-ink-2">{{ t('crypto.credited') }}</dt>
            <dd class="num" :class="isPositive(order.credited_amount) ? 'text-ok' : 'text-ink-3'" data-testid="crypto-credited">{{ usd(order.credited_amount) }}</dd>
          </div>
        </dl>
        <p v-if="paidFull" class="mt-3 text-sm text-ok" data-testid="crypto-paid-full">{{ t('crypto.paidFull') }}</p>
        <p v-else-if="remaining" class="mt-3 text-sm text-ink-2" data-testid="crypto-remaining">{{ t('crypto.remaining', { amount: usd(remaining) }) }}</p>
        <p v-if="hasHeld" class="mt-2 text-sm text-warn" role="status" data-testid="crypto-held">{{ t('crypto.heldNote') }}</p>
        <p v-if="hasOrphaned" class="mt-2 text-sm text-ink-3">{{ t('crypto.orphanedNote') }}</p>
      </section>

      <!-- Inflows -->
      <section class="mt-12" aria-labelledby="inflows-title">
        <h2 id="inflows-title" class="text-md font-semibold">{{ t('crypto.inflows') }}</h2>
        <p v-if="!order.deposits.length" class="mt-3 border-t border-rule-strong py-4 text-sm text-ink-3">{{ t('crypto.noInflows') }}</p>
        <div v-else class="overflow-x-auto">
          <table class="ledger mt-3" data-testid="crypto-deposits">
            <thead>
              <tr>
                <th>{{ t('crypto.when') }}</th>
                <th class="hidden sm:table-cell">{{ t('crypto.tx') }}</th>
                <th>{{ t('billing.status') }}</th>
                <th class="amount">{{ t('crypto.received') }}</th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="d in order.deposits" :key="d.id">
                <td class="num text-ink-2">
                  <span class="whitespace-nowrap">{{ dateTime(d.block_time, intlLocale()) }}</span>
                  <span class="mt-0.5 block whitespace-nowrap font-mono text-xs sm:hidden">
                    <a v-if="txUrl(d.tx_hash)" :href="txUrl(d.tx_hash)" target="_blank" rel="noopener noreferrer" :title="d.tx_hash">{{ shortHash(d.tx_hash) }}</a>
                    <span v-else :title="d.tx_hash">{{ shortHash(d.tx_hash) }}</span>
                  </span>
                </td>
                <td class="hidden font-mono text-xs leading-5 sm:table-cell">
                  <a v-if="txUrl(d.tx_hash)" :href="txUrl(d.tx_hash)" target="_blank" rel="noopener noreferrer" :title="d.tx_hash">{{ shortHash(d.tx_hash) }}</a>
                  <span v-else :title="d.tx_hash">{{ shortHash(d.tx_hash) }}</span>
                </td>
                <td><span class="tag" :class="depositTag(d.status)">{{ t(`crypto.status_${d.status}`) }}</span></td>
                <td class="amount num"><span class="whitespace-nowrap">{{ usd(d.amount) }}</span> <span class="block text-xs text-ink-3 sm:inline">{{ d.asset }}</span></td>
              </tr>
            </tbody>
          </table>
        </div>
      </section>
    </template>
  </section>
</template>
