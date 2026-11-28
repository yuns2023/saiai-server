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
import { marketApi, type OwnerMarketUpdate, type OwnerMarketView } from '@/api/market'
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

const props = defineProps<{ view: OwnerMarketView }>()
const emit = defineEmits<{ updated: [view: OwnerMarketView]; reload: [] }>()
const hosted = useHostedStore()
/**
 * One listing action at a time on the page, shared with pause / resume /
 * take-down: hosted.ownerBusy, taken and released right here (the release
 * must not depend on this component still being mounted when the answer comes).
 */
const busy = computed(() => hosted.ownerBusy !== null)
let mounted = true
onUnmounted(() => {
  mounted = false
})
const { t, te } = useI18n()
const app = useAppStore()

const state = computed(() => shareState(props.view))
const closed = computed(() => marketClosed(props.view))
const reasons = computed(() => visibleReasons(props.view))
const tiers = computed(() => props.view.allowed_tiers)
const pickable = computed(() => pickableTiers(props.view))
const usage = computed(() => upstreamUsage(props.view))
const cooldown = computed(() => props.view.cooldown_hours)

/** The server asked for an explicit tier (MARKET_TIER_REQUIRED): the picker shows even if one tier is known here. */
const tierRequired = ref(false)
/** Several tiers in the 套餐: the owner picks one and the switch names it; with one, the server uses it. */
const needsPick = computed(() => state.value === 'selfUse' && (tiers.value.length > 1 || tierRequired.value))
const choice = ref<number | null>(null)
const confirming = ref<Direction | null>(null)
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

const canShare = computed(() => props.view.can_list && (!needsPick.value || (choice.value != null && pickable.value.some((x) => x.group_id === choice.value))))
const canSelfUse = computed(() => props.view.can_unlist)

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

function ask(direction: Direction): void {
  if (busy.value) return
  confirming.value = direction
}

async function confirmSwitch(): Promise<void> {
  const direction = confirming.value
  if (!direction) return
  await send(direction === 'share')
  confirming.value = null
}

async function send(toShared: boolean): Promise<void> {
  if (hosted.ownerBusy !== null) return
  error.value = ''
  cooldownHit.value = false
  cooldownAt.value = null
  const body: OwnerMarketUpdate = !toShared
    ? { listed: false }
    : needsPick.value && choice.value != null
      ? { listed: true, group_id: choice.value }
      : { listed: true }
  hosted.ownerBusy = props.view.account_id
  const session = hosted.session()
  try {
    const view = await marketApi.setListing(props.view.account_id, body)
    tierRequired.value = false
    emit('updated', view)
    app.notify('ok', t(toShared ? 'market.sharedDone' : 'market.selfUseDone'))
  } catch (e) {
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
      <p class="mt-1 text-xs text-ink-2" data-testid="market-mode-hint">{{ state === 'shared' ? t('market.sharedHint') : t('market.selfUseHint') }}</p>
      <p v-if="closed" class="mt-1 text-xs text-warn" data-testid="market-closed">{{ t('market.marketClosed') }}</p>
      <ul v-if="reasons.length" class="mt-2 space-y-0.5 text-xs text-warn" data-testid="market-reasons">
        <li v-for="r in reasons" :key="r">· {{ t(reasonKey(r)) }}</li>
      </ul>

      <!-- Upstream limit windows of this account -->
      <p class="mt-2 flex flex-wrap gap-x-3 gap-y-0.5 text-xs text-ink-2" data-testid="market-upstream">
        <span>{{ t('market.upstream') }}</span>
        <span class="num" data-testid="market-upstream-5h">{{ windowLabel('five_hour') }} {{ pct(usage.fiveHour) }}</span>
        <span class="num" data-testid="market-upstream-7d">{{ windowLabel('seven_day') }} {{ pct(usage.sevenDay) }}</span>
      </p>

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

    <p v-if="view.next_switch_at || (state !== 'notGranted' && cooldown > 0)" class="mt-2 flex flex-wrap gap-x-3 gap-y-0.5 text-xs text-ink-3">
      <span v-if="view.next_switch_at" :title="view.next_switch_at" data-testid="market-next-switch">{{ t('market.nextSwitch', { time: when(view.next_switch_at) }) }}</span>
      <span v-if="state !== 'notGranted' && cooldown > 0" data-testid="market-cooldown-rule">{{ t('market.cooldownRule', { hours: cooldown }) }}</span>
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
      @cancel="confirming = null"
    />
  </div>
</template>
