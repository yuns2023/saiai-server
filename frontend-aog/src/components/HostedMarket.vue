<script setup lang="ts">
/**
 * 共享出售 for one owned account: 自用 (not on the market) or 共享中, its
 * 套餐 and tier, when the next switch is allowed, the upstream 5h / 7d usage
 * and the switch itself (confirmed first). An account without a 套餐 says
 * 未开通; a closed market says so on its own line.
 * The parent owns the view; this component sends the switch and hands the
 * server's answer back (`updated`), or asks for a fresh read (`reload`).
 */
import { computed, onUnmounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { marketApi, type MarketTierOption, type OwnerMarketUpdate, type OwnerMarketView } from '@/api/market'
import ConfirmDialog from '@/components/ConfirmDialog.vue'
import { intlLocale } from '@/i18n'
import { dateTime, rate } from '@/lib/format'
import {
  errorNextSwitchAt,
  errorReason,
  isCooldownError,
  isTierRequiredError,
  marketClosed,
  marketErrorKey,
  pickableTiers,
  reasonKey,
  shareState,
  upstreamUsage,
  visibleReasons
} from '@/lib/market'
import { useAppStore } from '@/stores/app'
import { useHostedStore } from '@/stores/hosted'

type Direction = 'share' | 'self'

/**
 * `apply` hands a PUT answer to the parent and says whether it was adopted (an answer older than
 * the view on screen is not: the parent reads again instead, and no success is announced).
 */
const props = defineProps<{ view: OwnerMarketView; apply: (view: OwnerMarketView) => boolean }>()
const emit = defineEmits<{ reload: [] }>()
const hosted = useHostedStore()
/**
 * One listing action at a time on the page, shared with pause / resume /
 * take-down: hosted.ownerBusy, taken and released right here (the release
 * must not depend on this component still being mounted when the answer comes).
 */
const busy = computed(() => hosted.ownerBusy !== null)
let mounted = true
/** Re-read when the cooldown ends, so the switch unlocks without a page reload. */
let cooldownTimer: ReturnType<typeof setTimeout> | null = null
/** Re-reads after the cooldown time passed but the switch is still closed: per next_switch_at, at most 3. */
const COOLDOWN_RETRY_MS = [5_000, 15_000, 60_000]
let retryFor: string | null = null
let retries = 0
onUnmounted(() => {
  mounted = false
  if (cooldownTimer) clearTimeout(cooldownTimer)
})
const { t, te } = useI18n()
const app = useAppStore()

const state = computed(() => shareState(props.view))
const closed = computed(() => marketClosed(props.view))
/** The owner paused the account: a shared one takes no orders now (said in the mode line). */
const paused = computed(() => props.view.unavailable_reasons.includes('owner_paused'))
/** Reason texts, each once (several configuration reasons share one neutral text); a shared card says "paused" itself. */
const reasons = computed(() => [
  ...new Set(
    visibleReasons(props.view)
      .filter((r) => !(state.value === 'shared' && r === 'owner_paused'))
      .map((r) => t(reasonKey(r)))
  )
])
/** Shared while the market is closed and switching back is open now (no cooldown applies). */
const closedCanLeave = computed(() => state.value === 'shared' && closed.value && props.view.can_unlist)
/** What the mode line says: a shared account that takes no orders now (paused, market closed) says so. */
const modeHintKey = computed(() => {
  if (state.value !== 'shared') return 'market.selfUseHint'
  if (paused.value) return 'market.sharedPausedHint'
  return closed.value ? 'market.sharedIdleHint' : 'market.sharedHint'
})
const tiers = computed(() => props.view.allowed_tiers)
const pickable = computed(() => pickableTiers(props.view))
const usage = computed(() => upstreamUsage(props.view))
const cooldown = computed(() => props.view.cooldown_hours)

/**
 * The server asked for an explicit tier (MARKET_TIER_REQUIRED): the picker shows even if one tier is
 * known here, until the listing changes (a new version decides again from its own tier list).
 */
const tierRequired = ref(false)
/** Several tiers in the 套餐: the owner picks one and the switch names it; with one, the server uses it. */
const needsPick = computed(() => state.value === 'selfUse' && (tiers.value.length > 1 || tierRequired.value))
const choice = ref<number | null>(null)
const confirming = ref<Direction | null>(null)
/**
 * What the owner confirms: the body built when the dialog opened, the listing version it was built
 * from and the tier (id + rate) shown. If the view changes meanwhile (new version, tier swapped or
 * repriced, permission lost), nothing is sent.
 */
let asked: { version: number; body: OwnerMarketUpdate; tier: { id: number; rate: number } | null } | null = null
/** This card's own switch is in flight (the dialog then stays open; Esc does not hide it). */
const sending = ref(false)
const error = ref('')
/** A cooldown refusal: its time comes from the error, else from the view read again right after. */
const cooldownHit = ref(false)
const cooldownAt = ref<string | null>(null)

// Preselect the current tier, else the only pickable one.
watch(
  () => [props.view.selected_group_id, props.view.version, props.view.allowed_tiers] as const,
  () => {
    const sel = props.view.selected_group_id
    if (sel != null && pickable.value.some((x) => x.group_id === sel)) choice.value = sel
    else if (choice.value == null || !pickable.value.some((x) => x.group_id === choice.value)) choice.value = pickable.value.length === 1 ? pickable.value[0].group_id : null
  },
  { immediate: true }
)

watch(
  () => props.view.version,
  () => {
    tierRequired.value = false
  }
)

const canShare = computed(() => props.view.can_list && (!needsPick.value || (choice.value != null && pickable.value.some((x) => x.group_id === choice.value))))
const canSelfUse = computed(() => props.view.can_unlist)
/** The switch this card offers is open now. */
const switchOpen = computed(() => (state.value === 'shared' ? props.view.can_unlist : state.value === 'selfUse' && props.view.can_list))

/**
 * The tier a share would sell in, as shown to the owner: the picked one with several, the only one
 * otherwise (shown read-only, the PUT then names none). null when there is none to show.
 */
const sellTier = computed<MarketTierOption | null>(() => {
  if (needsPick.value) return tiers.value.find((x) => x.group_id === choice.value) ?? null
  return tiers.value.length === 1 ? tiers.value[0] : null
})

function reloadLater(ms: number): void {
  if (cooldownTimer) clearTimeout(cooldownTimer)
  cooldownTimer = setTimeout(() => {
    cooldownTimer = null
    if (mounted) emit('reload')
  }, ms)
}

/**
 * Every new view: when the cooldown ends, read it again (the server decides whether the switch is
 * open). If the time already passed and the switch is still closed (clock skew, a late write), read
 * again after 5 s, 15 s, 60 s, then stop until the time changes. A view that opens the switch also
 * clears a cooldown refusal shown earlier.
 */
watch(
  () => props.view,
  (v) => {
    if (cooldownTimer) clearTimeout(cooldownTimer)
    cooldownTimer = null
    if (cooldownHit.value && (!v.next_switch_at || switchOpen.value)) {
      cooldownHit.value = false
      cooldownAt.value = null
    }
    const at = v.next_switch_at
    const ms = at ? Date.parse(at) - Date.now() : NaN
    if (!at || !Number.isFinite(ms)) {
      retryFor = null
      return
    }
    if (retryFor !== at) {
      retryFor = at
      retries = 0
    }
    // A second late (clock skew); beyond the longest cooldown (168 h) a reload of the page will do.
    if (ms > 0) {
      if (ms <= 169 * 3600_000) reloadLater(ms + 1000)
      return
    }
    if (switchOpen.value || state.value === 'notGranted' || retries >= COOLDOWN_RETRY_MS.length) return
    reloadLater(COOLDOWN_RETRY_MS[retries++])
  },
  { immediate: true }
)

function when(iso: string): string {
  return dateTime(iso, intlLocale())
}

const errorText = computed(() => {
  if (!cooldownHit.value) return error.value
  const at = cooldownAt.value ?? props.view.next_switch_at
  return at ? t('market.cooldownHit', { time: when(at) }) : t('market.cooldownHitNoTime')
})

function tagClass(): string {
  if (state.value === 'shared') return 'tag-ok'
  return 'tag-off'
}

function tierName(id: number | null): string {
  const tier = tiers.value.find((x) => x.group_id === id)
  return tier ? `${tier.name} ${rate(tier.rate_multiplier)}` : id != null ? `#${id}` : '—'
}

function windowLabel(key: string): string {
  return te(`hosted.window_${key}`) ? t(`hosted.window_${key}`) : key
}

function pct(u: number | null): string {
  return u == null ? t('market.usageUnknown') : `${Math.round(u)}%`
}

const confirmBody = computed(() => {
  const hours = cooldown.value
  if (confirming.value === 'self') return hours > 0 ? t('market.confirmSelfBody', { hours }) : t('market.confirmSelfBodyNoCooldown')
  return hours > 0 ? t('market.confirmShareBody', { hours }) : t('market.confirmShareBodyNoCooldown')
})

function tierLabel(tier: MarketTierOption): string {
  return `${tier.name} ${rate(tier.rate_multiplier)}`
}

function buildBody(direction: Direction): OwnerMarketUpdate {
  if (direction === 'self') return { listed: false }
  return needsPick.value && choice.value != null ? { listed: true, group_id: choice.value } : { listed: true }
}

/** The switch is still allowed by the view on screen (the dialog may have stayed open across a re-read). */
function stillAllowed(direction: Direction): boolean {
  return direction === 'self' ? state.value === 'shared' && canSelfUse.value : state.value === 'selfUse' && canShare.value
}

function shownTier(direction: Direction): { id: number; rate: number } | null {
  const tier = direction === 'share' ? sellTier.value : null
  return tier ? { id: tier.group_id, rate: tier.rate_multiplier } : null
}

/** The tier named in the open confirmation (as it was when the dialog opened). */
const confirmTier = ref('')

function ask(direction: Direction): void {
  if (busy.value || !stillAllowed(direction)) return
  asked = { version: props.view.version, body: buildBody(direction), tier: shownTier(direction) }
  confirmTier.value = direction === 'share' && sellTier.value ? tierLabel(sellTier.value) : ''
  confirming.value = direction
}

function cancelConfirm(): void {
  if (sending.value) return
  confirming.value = null
  asked = null
}

async function confirmSwitch(): Promise<void> {
  const direction = confirming.value
  const pending = asked
  if (!direction || !pending) return
  // The listing changed while the dialog was open: what was confirmed may no longer be what would be sent.
  const tierNow = shownTier(direction)
  const tierChanged = JSON.stringify(tierNow) !== JSON.stringify(pending.tier)
  const now = buildBody(direction)
  if (tierChanged || props.view.version !== pending.version || !stillAllowed(direction) || JSON.stringify(now) !== JSON.stringify(pending.body)) {
    confirming.value = null
    asked = null
    error.value = t(tierChanged ? 'market.tierChangedReconfirm' : 'market.changedReconfirm')
    cooldownHit.value = false
    return
  }
  sending.value = true
  try {
    await send(direction === 'share', pending.body)
  } finally {
    sending.value = false
    confirming.value = null
    asked = null
  }
}

async function send(toShared: boolean, body: OwnerMarketUpdate): Promise<void> {
  if (hosted.ownerBusy !== null) return
  error.value = ''
  cooldownHit.value = false
  cooldownAt.value = null
  hosted.ownerBusy = props.view.account_id
  const session = hosted.session()
  try {
    const view = await marketApi.setListing(props.view.account_id, body)
    // Signed out / another user meanwhile: this answer is not theirs to see.
    if (hosted.session() !== session) return
    tierRequired.value = false
    // Announced only when the page took this answer (an older one is replaced by a fresh read).
    if (props.apply(view)) app.notify('ok', t(toShared ? 'market.sharedDone' : 'market.selfUseDone'))
  } catch (e) {
    if (hosted.session() !== session) return
    if (isCooldownError(e)) {
      cooldownHit.value = true
      cooldownAt.value = errorNextSwitchAt(e)
    } else {
      // Several tiers after all: show the picker (the re-read below brings the list).
      if (isTierRequiredError(e)) tierRequired.value = true
      const key = marketErrorKey(e)
      const r = errorReason(e)
      error.value = key === 'market.errors.MARKET_ACCOUNT_NOT_ELIGIBLE' && r ? t(key) + t('market.reasonSep') + t(reasonKey(r)) : t(key)
    }
    // Also page-level: the re-read below may change this block (e.g. revoked), the message must stay visible.
    app.notify('error', errorText.value)
    // The server state may differ from what is shown (cooldown, revoked, tier gone…): read it again.
    emit('reload')
  } finally {
    if (hosted.session() === session) {
      hosted.ownerBusy = null
      // Answered after this page was left: the page on screen now re-reads this account.
      if (!mounted) hosted.notifyListingChanged(props.view.account_id)
    }
  }
}
</script>

<template>
  <div class="mx-3 mb-4 ml-10 border-l-2 border-rule pl-4" :data-testid="`market-${view.account_id}`">
    <div class="flex flex-wrap items-center gap-x-2 gap-y-1">
      <h3 class="text-sm font-semibold">{{ t('market.sectionTitle') }}</h3>
      <span class="tag" :class="tagClass()" data-testid="market-state">{{ t(`market.state_${state}`) }}</span>
      <span v-if="state === 'shared' && view.selected_group_id != null" class="text-xs text-ink-2" data-testid="market-current-tier">
        {{ t('market.currentTier', { tier: tierName(view.selected_group_id) }) }}
      </span>
      <span v-if="state !== 'notGranted' && view.plan" class="text-xs text-ink-3" data-testid="market-plan">{{ t('market.plan', { plan: view.plan }) }}</span>
    </div>

    <template v-if="state === 'notGranted'">
      <p class="mt-1 text-xs text-ink-3" data-testid="market-not-granted">{{ t('market.notGranted') }}</p>
      <p v-if="closed" class="mt-1 text-xs text-ink-3" data-testid="market-closed">{{ t('market.marketClosed') }}</p>
    </template>

    <template v-else>
      <p class="mt-1 text-xs text-ink-2" data-testid="market-mode-hint">{{ t(modeHintKey) }}</p>
      <p v-if="state === 'selfUse' && !needsPick && sellTier" class="mt-1 text-xs text-ink-2" data-testid="market-sell-tier">
        {{ t('market.sellTier', { tier: tierLabel(sellTier) }) }}
        <span v-if="!sellTier.available" class="text-ink-3">· {{ t('market.tierGone') }}</span>
        <span v-else-if="sellTier.admin_member" class="text-ink-3">· {{ t('market.tierAdminMember') }}</span>
      </p>
      <p v-if="closed" class="mt-1 text-xs text-warn" data-testid="market-closed">{{ closedCanLeave ? t('market.marketClosedShared') : t('market.marketClosed') }}</p>
      <ul v-if="reasons.length" class="mt-2 space-y-0.5 text-xs text-warn" data-testid="market-reasons">
        <li v-for="r in reasons" :key="r">· {{ r }}</li>
      </ul>
    </template>

    <!-- Upstream limit windows of this account -->
    <p class="mt-2 flex flex-wrap gap-x-3 gap-y-0.5 text-xs text-ink-2" data-testid="market-upstream">
      <span>{{ t('market.upstream') }}</span>
      <span class="num" data-testid="market-upstream-5h">{{ windowLabel('five_hour') }} {{ pct(usage.fiveHour) }}</span>
      <span class="num" data-testid="market-upstream-7d">{{ windowLabel('seven_day') }} {{ pct(usage.sevenDay) }}</span>
    </p>

    <template v-if="state !== 'notGranted'">
      <fieldset v-if="needsPick && tiers.length" class="mt-3" :disabled="busy">
        <legend class="label">{{ t('market.pickTier') }}</legend>
        <div class="mt-1 flex flex-wrap gap-2" role="radiogroup" :aria-label="t('market.pickTier')">
          <label
            v-for="tier in tiers"
            :key="tier.group_id"
            class="flex cursor-pointer items-center gap-2 rounded border px-3 py-1.5 text-sm"
            :class="[
              choice === tier.group_id ? 'border-blue bg-blue-wash' : 'border-rule',
              !tier.available || tier.admin_member ? 'cursor-not-allowed opacity-60' : ''
            ]"
            :data-testid="`market-tier-${tier.group_id}`"
          >
            <input
              v-model="choice"
              type="radio"
              :name="`market-tier-${view.account_id}`"
              :value="tier.group_id"
              :disabled="!tier.available || tier.admin_member"
              class="accent-[var(--blue)]"
            />
            <span>{{ tier.name }}</span>
            <span class="num text-ink-2">{{ rate(tier.rate_multiplier) }}</span>
            <span v-if="!tier.available" class="text-xs text-ink-3">{{ t('market.tierGone') }}</span>
            <span v-else-if="tier.admin_member" class="text-xs text-ink-3">{{ t('market.tierAdminMember') }}</span>
          </label>
        </div>
      </fieldset>

      <div class="mt-3 flex flex-wrap items-center gap-x-3 gap-y-2">
        <button
          v-if="state === 'selfUse'"
          type="button"
          class="btn btn-primary btn-sm"
          :disabled="busy || !canShare"
          data-testid="market-share"
          @click="ask('share')"
        >
          {{ t('market.startShare') }}
        </button>
        <button
          v-else
          type="button"
          class="btn btn-line btn-sm"
          :disabled="busy || !canSelfUse"
          data-testid="market-self-use"
          @click="ask('self')"
        >
          {{ t('market.backToSelf') }}
        </button>
        <span v-if="needsPick && view.can_list && choice == null && pickable.length > 1" class="text-xs text-ink-3">{{ t('market.pickFirst') }}</span>
      </div>
    </template>

    <!-- Shared on a closed market with switching back open: no cooldown applies, so no time / rule. -->
    <p v-if="!closedCanLeave && (view.next_switch_at || cooldown > 0)" class="mt-2 flex flex-wrap gap-x-3 gap-y-0.5 text-xs text-ink-3">
      <span v-if="view.next_switch_at" :title="view.next_switch_at" data-testid="market-next-switch">{{ t('market.nextSwitch', { time: when(view.next_switch_at) }) }}</span>
      <span v-if="cooldown > 0" data-testid="market-cooldown-rule">{{ t('market.cooldownRule', { hours: cooldown }) }}</span>
    </p>
    <p v-if="errorText" class="mt-2 text-sm text-seal" role="alert" data-testid="market-error">{{ errorText }}</p>

    <p v-if="state !== 'notGranted'" class="mt-3 max-w-[70ch] text-xs leading-5 text-ink-3" data-testid="market-explain">{{ t('market.explain') }}</p>

    <ConfirmDialog
      :open="confirming !== null"
      :title="confirming === 'self' ? t('market.confirmSelfTitle') : t('market.confirmShareTitle')"
      :body="confirmBody"
      :confirm-label="confirming === 'self' ? t('market.backToSelf') : t('market.startShare')"
      :busy="busy"
      @confirm="confirmSwitch"
      @cancel="cancelConfirm"
    >
      <p v-if="confirming === 'share' && confirmTier" class="mt-2 text-sm text-ink" data-testid="market-confirm-tier">{{ t('market.sellTier', { tier: confirmTier }) }}</p>
    </ConfirmDialog>
  </div>
</template>
