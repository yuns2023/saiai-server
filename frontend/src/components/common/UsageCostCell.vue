<template>
  <div class="text-sm" data-testid="usage-cost-cell">
    <div class="flex flex-wrap items-center gap-x-1.5 gap-y-0.5 md:flex-nowrap md:whitespace-nowrap">
      <!-- Billing source: icon + colour, full label in tooltip / accessible name -->
      <HelpTooltip lazy class="!ml-0">
        <template #trigger>
          <button
            type="button"
            :class="['usage-cost-icon', billing.class]"
            :aria-label="billingLabel"
            data-testid="usage-cost-billing-icon"
            :data-billing="billing.kind"
          >
            <Icon :name="billing.icon" size="sm" />
          </button>
        </template>
        {{ billingLabel }}
      </HelpTooltip>
      <span class="font-medium tabular-nums text-gray-900 dark:text-white" data-testid="usage-cost-amount">${{ (row.actual_cost ?? 0).toFixed(6) }}</span>

      <!-- Multipliers that change the price stay as short chips -->
      <span v-if="row.model_rate_multiplier != null && row.model_rate_multiplier !== 1" class="rounded-sm bg-indigo-100 px-1 text-[10px] text-indigo-700 dark:bg-indigo-500/20 dark:text-indigo-300" :title="t('usage.groupModelMultiplier')">{{ row.model_rate_multiplier }}x</span>
      <span v-if="row.user_payg_discount_multiplier != null && row.user_payg_discount_multiplier !== 1" class="rounded-sm bg-emerald-100 px-1 text-[10px] text-emerald-700 dark:bg-emerald-500/20 dark:text-emerald-300" :title="t('usage.paygUserDiscount')">{{ row.user_payg_discount_multiplier }}x</span>
      <span v-if="row.account_payg_discount_multiplier != null && row.account_payg_discount_multiplier !== 1" class="rounded-sm bg-amber-100 px-1 text-[10px] text-amber-700 dark:bg-amber-500/20 dark:text-amber-300" :title="t('usage.legacyPaygAccountDiscount')">{{ row.account_payg_discount_multiplier }}x</span>

      <!-- Service tier: hidden for Standard/default, small icon otherwise -->
      <HelpTooltip v-if="tier" lazy class="!ml-0">
        <template #trigger>
          <button
            type="button"
            :class="['usage-cost-icon', tier.class]"
            :aria-label="tierLabel"
            data-testid="usage-cost-tier-icon"
            :data-tier="tier.kind"
          >
            <Icon :name="tier.icon" size="xs" />
          </button>
        </template>
        {{ tierLabel }}
      </HelpTooltip>

      <!-- Secondary reference amounts: compact grey, explained in a tooltip -->
      <HelpTooltip v-if="showReference && row.metered_cost != null" lazy class="!ml-0">
        <template #trigger>
          <button
            type="button"
            class="usage-cost-secondary"
            :aria-label="`${t('hostedUsage.reference')} $${row.metered_cost.toFixed(6)}: ${t('hostedUsage.hint')}`"
            data-testid="usage-cost-reference"
          >
            ≈${{ row.metered_cost.toFixed(6) }}
          </button>
        </template>
        <span class="font-semibold">{{ t('hostedUsage.reference') }}</span>: {{ t('hostedUsage.hint') }}
      </HelpTooltip>
      <HelpTooltip v-if="accountCost != null" lazy class="!ml-0">
        <template #trigger>
          <button
            type="button"
            class="usage-cost-secondary"
            :aria-label="`${t('usage.accountBilled')} $${accountCost.toFixed(6)}: ${t('usage.accountBilledHint')}`"
            data-testid="usage-cost-account"
          >
            A ${{ accountCost.toFixed(6) }}
          </button>
        </template>
        <span class="font-semibold">{{ t('usage.accountBilled') }}</span>: {{ t('usage.accountBilledHint') }}
      </HelpTooltip>

      <!-- Cost breakdown (rendered by the parent view) -->
      <button
        ref="detailButton"
        type="button"
        class="flex h-4 w-4 cursor-help items-center justify-center rounded-full bg-gray-100 text-gray-400 transition-colors hover:bg-blue-100 hover:text-blue-500 focus:outline-hidden focus-visible:ring-2 focus-visible:ring-primary-500/50 dark:bg-gray-700 dark:text-gray-500 dark:hover:bg-blue-900/50 dark:hover:text-blue-400"
        :aria-label="t('usage.costDetails')"
        :aria-expanded="detailOpen"
        data-testid="usage-cost-detail"
        @mouseenter="detailOn('hover', $event)"
        @mouseleave="detailOff('hover')"
        @focus="detailOn('focus', $event)"
        @blur="detailOff('focus')"
        @click="onDetailClick"
      >
        <Icon name="infoCircle" size="xs" />
      </button>
    </div>
    <slot />
  </div>
</template>

<script setup lang="ts">
import { computed, ref, useTemplateRef, watch, onBeforeUnmount } from 'vue'
import { useI18n } from 'vue-i18n'
import HelpTooltip from './HelpTooltip.vue'
import Icon from '@/components/icons/Icon.vue'
import { usageBillingLabel } from '@/utils/usageBilling'
import { isNativeChatUsage } from '@/utils/nativeChatUsage'
import { formatUsageServiceTier, getUsageServiceTierLabel } from '@/utils/usageServiceTier'
import { usageBillingVisual, usageServiceTierVisual } from '@/utils/usageCostVisual'

type CostRow = {
  billing_type?: number
  metered_cost?: number | null
  service_tier?: string | null
  model_rate_multiplier?: number | null
  user_payg_discount_multiplier?: number | null
  account_payg_discount_multiplier?: number | null
} & Parameters<typeof isNativeChatUsage>[0]

const props = withDefaults(defineProps<{
  row: CostRow
  /** Platform account cost (admin only); omitted/null hides the "A" amount. */
  accountCost?: number | null
  /** Show the hosted-usage reference amount (metered_cost) when present. */
  showReference?: boolean
  /**
   * Whether the parent's single shared breakdown panel currently belongs to this row.
   * When ownership moves to another row, this cell drops its hover/focus/pin state
   * silently so that hovering it again re-claims the panel.
   */
  detailActive?: boolean
}>(), { accountCost: null, showReference: true, detailActive: true })

const emit = defineEmits<{
  (e: 'show-detail', event: Event): void
  (e: 'hide-detail'): void
}>()

const { t } = useI18n()

const billing = computed(() => usageBillingVisual(props.row.billing_type))
const billingLabel = computed(() => t(usageBillingLabel(props.row.billing_type)))

const tier = computed(() => (isNativeChatUsage(props.row) ? null : usageServiceTierVisual(props.row.service_tier)))
const tierLabel = computed(() => `${t('usage.serviceTier')}: ${getUsageServiceTierLabel(formatUsageServiceTier(props.row.service_tier), t)}`)

// Cost breakdown panel (owned by the parent): hover, focus and tap are tracked
// separately; tap toggles, and an outside tap, Escape, scroll, resize or unmount closes it.
type DetailSource = 'hover' | 'focus' | 'pin'
const detailSources = ref(new Set<DetailSource>())
const detailOpen = computed(() => detailSources.value.size > 0)
const detailButton = useTemplateRef<HTMLButtonElement>('detailButton')

const setDetail = (next: Set<DetailSource>, event?: Event) => {
  const wasOpen = detailOpen.value
  detailSources.value = next
  if (detailOpen.value && !wasOpen && event) emit('show-detail', event)
  if (!detailOpen.value && wasOpen) emit('hide-detail')
}
const detailOn = (source: DetailSource, event: Event) => setDetail(new Set(detailSources.value).add(source), event)
const detailOff = (source: DetailSource) => {
  const next = new Set(detailSources.value)
  next.delete(source)
  setDetail(next)
}
const closeDetail = () => setDetail(new Set())
const onDetailClick = (event: Event) => {
  if (detailSources.value.has('pin')) closeDetail()
  else detailOn('pin', event)
}

const onOutsidePointer = (event: Event) => {
  if (!detailButton.value?.contains(event.target as Node)) closeDetail()
}
const onKeydown = (event: Event) => {
  if ((event as KeyboardEvent).key === 'Escape') closeDetail()
}
const setGlobalListeners = (active: boolean) => {
  const method = active ? 'addEventListener' : 'removeEventListener'
  document[method]('pointerdown', onOutsidePointer, true)
  document[method]('keydown', onKeydown, true)
  window[method]('scroll', closeDetail, true)
  // A fixed panel would detach from its row (or overflow) after a resize/rotation.
  window[method]('resize', closeDetail)
  window[method]('orientationchange', closeDetail)
}
watch(detailOpen, setGlobalListeners)
watch(
  () => props.detailActive,
  (active) => {
    if (!active && detailOpen.value) detailSources.value = new Set()
  },
)
onBeforeUnmount(() => {
  setGlobalListeners(false)
  if (detailOpen.value) emit('hide-detail')
})
</script>

<style scoped>
@reference "../../style.css";

.usage-cost-icon {
  @apply inline-flex h-5 w-5 shrink-0 cursor-help items-center justify-center rounded-md;
  @apply focus:outline-hidden focus-visible:ring-2 focus-visible:ring-primary-500/50;
}

.usage-cost-secondary {
  @apply cursor-help text-[11px] tabular-nums text-gray-400 underline decoration-dotted underline-offset-2 dark:text-gray-500;
  @apply focus:outline-hidden focus-visible:ring-2 focus-visible:ring-primary-500/50 rounded-sm;
}
</style>
