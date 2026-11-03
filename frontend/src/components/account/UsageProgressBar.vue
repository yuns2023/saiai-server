<template>
  <div :title="tooltip">
    <!-- Window stats row (above progress bar) -->
    <div
      v-if="(windowStats && hasActivity) || standardCost != null || estimatedCapacity != null"
      class="mb-0.5 flex items-center"
      data-testid="window-stats"
    >
      <div class="flex flex-wrap items-center gap-1.5 text-[9px] text-gray-500 dark:text-gray-400">
        <template v-if="windowStats && hasActivity">
          <span
            class="rounded-sm bg-gray-100 px-1.5 py-0.5 dark:bg-gray-800"
            :title="statsRolling ? t('admin.accounts.usageWindow.rollingHint') : undefined"
            data-testid="window-requests"
          >
            {{ formatRequests }} req{{ statsRolling ? '*' : '' }}
          </span>
          <span class="rounded-sm bg-gray-100 px-1.5 py-0.5 dark:bg-gray-800">
            {{ formatTokens }}
          </span>
          <span class="rounded-sm bg-gray-100 px-1.5 py-0.5 dark:bg-gray-800" :title="t('usage.accountBilled')">
            A ${{ formatAccountCost }}
          </span>
          <span
            v-if="windowStats?.user_cost != null"
            class="rounded-sm bg-gray-100 px-1.5 py-0.5 dark:bg-gray-800"
            :title="t('usage.userBilled')"
          >
            U ${{ formatUserCost }}
          </span>
        </template>
        <span
          v-if="standardCost != null"
          class="rounded-sm bg-gray-100 px-1.5 py-0.5 dark:bg-gray-800"
          :title="t('admin.accounts.usageWindow.standardCostHint')"
          data-testid="window-standard-cost"
        >
          {{ t('admin.accounts.usageWindow.standardCost', { cost: standardCost.toFixed(2) }) }}
        </span>
        <span
          v-if="estimatedCapacity != null"
          class="rounded-sm bg-gray-100 px-1.5 py-0.5 dark:bg-gray-800"
          :title="t('admin.accounts.usageWindow.estimatedCapacityHint')"
          data-testid="window-estimated-capacity"
        >
          {{ t('admin.accounts.usageWindow.estimatedCapacity', { cost: estimatedCapacity.toFixed(2) }) }}
        </span>
      </div>
    </div>

    <!-- Progress bar row -->
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
      <span v-if="shouldShowResetTime" class="shrink-0 text-[10px] text-gray-400">
        {{ formatResetTime }}
      </span>
    </div>
  </div>
</template>

<script setup lang="ts">
import { formatTokenCount } from '@/utils/usageTokens'
import { computed, ref, watch } from 'vue'
import { useIntervalFn } from '@vueuse/core'
import { useI18n } from 'vue-i18n'
import type { WindowStats } from '@/types'
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

const hasActivity = computed(() =>
  Boolean(props.windowStats && (props.windowStats.requests > 0 || props.windowStats.tokens > 0))
)

const formatUserCost = computed(() => {
  if (!props.windowStats || props.windowStats.user_cost == null) return '0.00'
  return props.windowStats.user_cost.toFixed(2)
})

</script>
