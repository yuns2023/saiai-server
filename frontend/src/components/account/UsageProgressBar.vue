<template>
  <div
    ref="rootRef"
    :title="tooltip"
    @mouseenter="openDetails"
    @mouseleave="detailsOpen = false"
  >
    <!-- One line per window: label, bar, percent, reset, A $ · 估 $ -->
    <div class="flex items-center gap-1">
      <!-- Label badge (fixed width for alignment) -->
      <span
        :class="['w-[32px] shrink-0 rounded-sm px-1 text-center text-[10px] font-medium', labelClass]"
      >
        {{ label }}
      </span>

      <!-- Progress bar container -->
      <div class="h-1.5 w-8 shrink-0 overflow-hidden rounded-full bg-gray-200 dark:bg-gray-700">
        <div
          :class="['h-full transition-all duration-300', barClass]"
          :style="{ width: barWidth }"
        ></div>
      </div>

      <!-- Percentage -->
      <span :class="['w-[32px] shrink-0 text-right text-[10px] font-medium', textClass]">
        {{ displayPercent }}
      </span>

      <!-- Reset time -->
      <span v-if="shouldShowResetTime" class="w-[42px] shrink-0 text-[10px] text-gray-400">
        {{ formatResetTime }}
      </span>

      <!-- Key figures; the rest is in the hover details -->
      <span
        v-if="showAccountCostInline || showEstimate"
        class="shrink-0 text-[10px] tabular-nums text-gray-500 dark:text-gray-400"
        data-testid="window-summary"
      >
        <template v-if="showAccountCostInline">A ${{ formatShortCost(windowStats!.cost) }}</template>
        <template v-if="showAccountCostInline && showEstimate"> · </template>
        <template v-if="showEstimate">{{ t('admin.accounts.usageWindow.estimatedCapacity', { cost: formatShortCost(estimatedCapacity ?? 0) }) }}</template>
      </span>

      <span
        v-if="showRestart"
        class="shrink-0 rounded-sm bg-sky-50 px-1 text-[10px] text-sky-700 dark:bg-sky-900/20 dark:text-sky-300"
        :title="restartHint"
        data-testid="window-restart"
      >
        ↺ {{ t('admin.accounts.usageWindow.restartedChip', { time: restartShort }) }}
      </span>
    </div>

    <!-- Hover details: fixed-positioned so the scrolling table does not clip it -->
    <div
      v-if="hasDetails"
      v-show="detailsOpen"
      class="fixed z-50 rounded-md border border-gray-200 bg-white px-2.5 py-2 text-[11px] text-gray-600 shadow-lg dark:border-dark-600 dark:bg-dark-800 dark:text-gray-300"
      :style="detailsStyle"
      data-testid="window-stats"
    >
      <div class="mb-1 font-medium text-gray-800 dark:text-gray-100">{{ label }}</div>
      <div class="grid grid-cols-[auto_auto] gap-x-3 gap-y-0.5 tabular-nums">
        <template v-if="windowStats && hasActivity">
          <span class="text-gray-400">req</span>
          <span
            :title="statsRolling ? t('admin.accounts.usageWindow.rollingHint') : t('admin.accounts.usageWindow.statsHint')"
            data-testid="window-requests"
          >{{ formatRequests }} req{{ statsRolling ? '*' : '' }}</span>
          <span class="text-gray-400">Token</span>
          <span>{{ formatTokens }}</span>
          <span class="text-gray-400" :title="t('usage.accountBilled')">A</span>
          <span>${{ formatAccountCost }}</span>
          <template v-if="windowStats?.user_cost != null">
            <span class="text-gray-400">U</span>
            <span data-testid="window-revenue">
              <RevenueAmount :parts="windowStats" :billed-cost="windowStats.user_cost" />
            </span>
          </template>
        </template>
        <template v-if="showStandardCost">
          <span class="text-gray-400" :title="t('admin.accounts.usageWindow.standardCostHint')">{{ t('admin.accounts.usageWindow.standardCostLabel') }}</span>
          <span data-testid="window-standard-cost">
            ${{ (standardCost ?? 0).toFixed(2) }}
          </span>
        </template>
        <template v-if="showEstimate">
          <span class="text-gray-400" :title="t('admin.accounts.usageWindow.estimatedCapacityHint')">{{ t('admin.accounts.usageWindow.estimatedCapacityLabel') }}</span>
          <span data-testid="window-estimated-capacity">
            ${{ (estimatedCapacity ?? 0).toFixed(2) }}<template v-if="estimateAge"> · {{ estimateAge }}</template>
          </span>
        </template>
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
import { formatTokenCount } from '@/utils/usageTokens'
import { computed, ref, watch } from 'vue'
import { useIntervalFn } from '@vueuse/core'
import { useI18n } from 'vue-i18n'
import type { UsageWindowRestart, WindowStats } from '@/types'
import RevenueAmount from '@/components/common/RevenueAmount.vue'
import { formatCompactNumber, formatCountdown } from '@/utils/format'

const props = defineProps<{
  label: string
  utilization: number // Percentage (0-100+)
  resetsAt?: string | null
  color: 'indigo' | 'emerald' | 'purple' | 'amber'
  tooltip?: string
  windowStats?: WindowStats | null
  // Window stats fell back to a rolling window because the reset time is unknown.
  statsRolling?: boolean
  // Platform usage at standard price in [reset - window, now); null/undefined = unknown.
  standardCost?: number | null
  // Full-window capacity estimate at standard price (null/undefined = unknown).
  estimatedCapacity?: number | null
  // When the estimate's utilization sample was taken (idle accounts keep an old one).
  estimateSampledAt?: string | null
  // Statistics restart point (upstream reset usage mid-window); stats start here.
  windowRestart?: UsageWindowRestart | null
  showNowWhenIdle?: boolean
}>()

const { t } = useI18n()

// Reactive clock for countdown — only runs when a reset time is shown,
// to avoid creating many idle timers across large account lists.
const now = ref(new Date())
const { pause: pauseClock, resume: resumeClock } = useIntervalFn(
  () => {
    now.value = new Date()
  },
  60_000,
  { immediate: false },
)
if (props.resetsAt) resumeClock()
watch(
  () => props.resetsAt,
  (val) => {
    if (val) {
      now.value = new Date()
      resumeClock()
    } else {
      pauseClock()
    }
  },
)

// Label background colors
const labelClass = computed(() => {
  const colors = {
    indigo: 'bg-indigo-100 text-indigo-700 dark:bg-indigo-900/40 dark:text-indigo-300',
    emerald: 'bg-emerald-100 text-emerald-700 dark:bg-emerald-900/40 dark:text-emerald-300',
    purple: 'bg-purple-100 text-purple-700 dark:bg-purple-900/40 dark:text-purple-300',
    amber: 'bg-amber-100 text-amber-700 dark:bg-amber-900/40 dark:text-amber-300'
  }
  return colors[props.color]
})

// Progress bar color based on utilization
const barClass = computed(() => {
  if (props.utilization >= 100) {
    return 'bg-red-500'
  } else if (props.utilization >= 80) {
    return 'bg-amber-500'
  } else {
    return 'bg-green-500'
  }
})

// Text color based on utilization
const textClass = computed(() => {
  if (props.utilization >= 100) {
    return 'text-red-600 dark:text-red-400'
  } else if (props.utilization >= 80) {
    return 'text-amber-600 dark:text-amber-400'
  } else {
    return 'text-gray-600 dark:text-gray-400'
  }
})

// Bar width (capped at 100%)
const barWidth = computed(() => {
  return `${Math.min(props.utilization, 100)}%`
})

// Display percentage (cap at 999% for readability)
const displayPercent = computed(() => {
  const percent = Math.round(props.utilization)
  return percent > 999 ? '>999%' : `${percent}%`
})

const shouldShowResetTime = computed(() => {
  if (props.resetsAt) return true
  return Boolean(props.showNowWhenIdle && props.utilization <= 0)
})

// Format reset time
const formatResetTime = computed(() => {
  // For rolling windows, when utilization is 0%, treat as immediately available.
  if (props.showNowWhenIdle && props.utilization <= 0) {
    return t('common.now')
  }

  if (!props.resetsAt) return '-'

  return formatCountdown(props.resetsAt, now.value) ?? t('common.now')
})

// Window stats formatters
const formatRequests = computed(() => {
  if (!props.windowStats) return ''
  return formatCompactNumber(props.windowStats.requests, { allowBillions: false })
})

const formatTokens = computed(() => {
  if (!props.windowStats) return ''
  return formatTokenCount(props.windowStats.tokens)
})

const formatAccountCost = computed(() => {
  if (!props.windowStats) return '0.00'
  return props.windowStats.cost.toFixed(2)
})

// Window costs belong to the window that was current when loaded; hide them
// once the countdown passes the reset (the parent may not have refreshed yet).
const windowStillCurrent = computed(
  () => !props.resetsAt || new Date(props.resetsAt).getTime() > now.value.getTime()
)
const showStandardCost = computed(() => props.standardCost != null && windowStillCurrent.value)
const showEstimate = computed(() => props.estimatedCapacity != null && windowStillCurrent.value)
// Age of the estimate's sample, shown once it is at least 15 minutes old.
const estimateAge = computed(() => {
  if (!props.estimateSampledAt) return ''
  const minutes = Math.floor((now.value.getTime() - new Date(props.estimateSampledAt).getTime()) / 60_000)
  if (!Number.isFinite(minutes) || minutes < 15) return ''
  if (minutes < 60) return t('common.time.minutesAgo', { n: minutes })
  if (minutes < 24 * 60) return t('common.time.hoursAgo', { n: Math.floor(minutes / 60) })
  return t('common.time.daysAgo', { n: Math.floor(minutes / (24 * 60)) })
})

const restartDate = computed(() => {
  if (!props.windowRestart?.start) return null
  const d = new Date(props.windowRestart.start)
  return Number.isNaN(d.getTime()) ? null : d
})
const showRestart = computed(() => restartDate.value != null && windowStillCurrent.value)
const restartShort = computed(() => {
  const d = restartDate.value
  if (!d) return ''
  const pad = (n: number) => String(n).padStart(2, '0')
  return `${pad(d.getMonth() + 1)}-${pad(d.getDate())} ${pad(d.getHours())}:${pad(d.getMinutes())}`
})
const restartHint = computed(() => {
  if (!restartDate.value) return ''
  const source = props.windowRestart?.source === 'auto'
    ? t('admin.accounts.usageWindow.restartSourceAuto')
    : t('admin.accounts.usageWindow.restartSourceManual')
  return t('admin.accounts.usageWindow.restartedHint', { time: restartDate.value.toLocaleString(), source })
})

const hasActivity = computed(() =>
  Boolean(props.windowStats && (props.windowStats.requests > 0 || props.windowStats.tokens > 0))
)

const showAccountCostInline = computed(() => Boolean(props.windowStats && hasActivity.value))

// Inline figures: whole dollars from $100 up, cents below.
const formatShortCost = (value: number) => (Math.abs(value) >= 100 ? value.toFixed(0) : value.toFixed(2))

const hasDetails = computed(
  () => Boolean(props.windowStats && hasActivity.value) || showStandardCost.value || showEstimate.value
)

const rootRef = ref<HTMLElement | null>(null)
const detailsOpen = ref(false)
const detailsStyle = ref<Record<string, string>>({})
const openDetails = () => {
  if (!hasDetails.value) return
  const rect = rootRef.value?.getBoundingClientRect()
  if (rect) {
    // Below the row; flip above when near the bottom of the viewport.
    const flip = rect.bottom + 150 > window.innerHeight
    detailsStyle.value = flip
      ? { left: `${rect.left}px`, bottom: `${window.innerHeight - rect.top + 4}px` }
      : { left: `${rect.left}px`, top: `${rect.bottom + 4}px` }
  }
  detailsOpen.value = true
}

</script>
