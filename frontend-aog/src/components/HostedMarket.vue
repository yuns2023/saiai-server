<script setup lang="ts">
/**
 * 共享出售 for one owned account: whether it may be shared, why not, the
 * tiers an administrator allowed (name + rate) and the list / unlist switch.
 * The parent owns the view; this component sends the change and hands the
 * server's answer back (`updated`), or asks for a fresh read (`reload`).
 */
import { computed, onUnmounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { marketApi, type OwnerMarketView } from '@/api/market'
import { rate } from '@/lib/format'
import { errorReason, marketErrorKey, marketState, pickableTiers, reasonKey, visibleReasons } from '@/lib/market'
import { useAppStore } from '@/stores/app'
import { useHostedStore } from '@/stores/hosted'

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

const state = computed(() => marketState(props.view))
const listed = computed(() => props.view.status === 'listed')
const reasons = computed(() => visibleReasons(props.view))
const tiers = computed(() => props.view.allowed_tiers)
const pickable = computed(() => pickableTiers(props.view))
const choice = ref<number | null>(null)
const error = ref('')

// Preselect the current tier, else the only pickable one.
watch(
  () => [props.view.selected_group_id, props.view.version, props.view.allowed_tiers] as const,
  () => {
    const sel = props.view.selected_group_id
    if (sel != null && tiers.value.some((x) => x.group_id === sel)) choice.value = sel
    else if (choice.value == null || !pickable.value.some((x) => x.group_id === choice.value)) choice.value = pickable.value.length === 1 ? pickable.value[0].group_id : null
  },
  { immediate: true }
)

const switching = computed(() => listed.value && choice.value != null && choice.value !== props.view.selected_group_id)
const canStart = computed(() => props.view.can_list && choice.value != null && pickable.value.some((x) => x.group_id === choice.value))

function tagClass(): string {
  if (state.value === 'listed') return 'tag-ok'
  if (state.value === 'canList') return 'tag-off'
  if (state.value === 'unavailable') return 'tag-warn'
  return 'tag-off'
}

function stateLabel(): string {
  if (state.value === 'unavailable' && listed.value) return `${t('market.state_listed')} · ${t('market.state_unavailable')}`
  return t(`market.state_${state.value}`)
}

async function send(listedNext: boolean): Promise<void> {
  if (hosted.ownerBusy !== null) return
  error.value = ''
  const body = listedNext ? { listed: true as const, group_id: choice.value as number } : { listed: false as const }
  const doneKey = listedNext ? (switching.value ? 'market.switchedDone' : 'market.listedDone') : 'market.unlistedDone'
  hosted.ownerBusy = props.view.account_id
  const session = hosted.session()
  try {
    const view = await marketApi.setListing(props.view.account_id, body)
    emit('updated', view)
    app.notify('ok', t(doneKey))
  } catch (e) {
    const key = marketErrorKey(e)
    const r = errorReason(e)
    error.value = key === 'market.errors.MARKET_ACCOUNT_NOT_ELIGIBLE' && r ? t(key) + t('market.reasonSep') + t(reasonKey(r)) : t(key)
    // Also page-level: the re-read below may remove this block (e.g. revoked), the message must stay visible.
    app.notify('error', error.value)
    // The server state may differ from what is shown (revoked, tier gone…): read it again.
    emit('reload')
  } finally {
    if (hosted.session() === session) {
      hosted.ownerBusy = null
      // Answered after this page was left: the page on screen now re-reads this account.
      if (!mounted) hosted.notifyListingChanged(props.view.account_id)
    }
  }
}

function tierName(id: number | null): string {
  const tier = tiers.value.find((x) => x.group_id === id)
  return tier ? `${tier.name} ${rate(tier.rate_multiplier)}` : id != null ? `#${id}` : '—'
}

const protection = computed(() => props.view.protection)
const hasText = (key: string) => te(key)
</script>

<template>
  <div class="mx-3 mb-4 ml-10 border-l-2 border-rule pl-4" :data-testid="`market-${view.account_id}`">
    <div class="flex flex-wrap items-center gap-x-2 gap-y-1">
      <h3 class="text-sm font-semibold">{{ t('market.sectionTitle') }}</h3>
      <span class="tag" :class="tagClass()" data-testid="market-state">{{ stateLabel() }}</span>
      <span v-if="listed && view.selected_group_id != null" class="text-xs text-ink-2" data-testid="market-current-tier">
        {{ t('market.currentTier', { tier: tierName(view.selected_group_id) }) }}
      </span>
    </div>

    <template v-if="state === 'notAllowed'">
      <p class="mt-1 text-xs text-ink-3" data-testid="market-not-allowed">
        {{ view.status === 'revoked' ? t('market.revokedHint') : t('market.notAllowedHint') }}
      </p>
    </template>

    <template v-else>
      <ul v-if="reasons.length" class="mt-2 space-y-0.5 text-xs text-warn" data-testid="market-reasons">
        <li v-for="r in reasons" :key="r">· {{ hasText(reasonKey(r)) ? t(reasonKey(r)) : r }}</li>
      </ul>

      <fieldset v-if="tiers.length" class="mt-3" :disabled="busy">
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
          v-if="!listed"
          type="button"
          class="btn btn-primary btn-sm"
          :disabled="busy || !canStart"
          data-testid="market-list"
          @click="send(true)"
        >
          {{ t('market.list') }}
        </button>
        <template v-else>
          <button
            v-if="switching"
            type="button"
            class="btn btn-primary btn-sm"
            :disabled="busy || !canStart"
            data-testid="market-switch"
            @click="send(true)"
          >
            {{ t('market.switchTier') }}
          </button>
          <button type="button" class="btn btn-line btn-sm" :disabled="busy" data-testid="market-unlist" @click="send(false)">
            {{ t('market.unlist') }}
          </button>
        </template>
        <span v-if="!listed && view.can_list && choice == null && pickable.length > 1" class="text-xs text-ink-3">{{ t('market.pickFirst') }}</span>
      </div>
      <p v-if="error" class="mt-2 text-sm text-seal" role="alert" data-testid="market-error">{{ error }}</p>

      <p class="mt-3 max-w-[70ch] text-xs leading-5 text-ink-3" data-testid="market-explain">
        {{ t('market.explain') }}
        <template v-if="protection">
          {{
            t('market.protection', {
              concurrency: protection.reserved_concurrency,
              devices: protection.reserved_devices,
              admissions: protection.reserved_admissions,
              cap5h: protection.cap_5h_pct,
              cap7d: protection.cap_7d_pct
            })
          }}
        </template>
      </p>
    </template>
  </div>
</template>
