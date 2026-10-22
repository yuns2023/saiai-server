import { formatUsageServiceTier } from './usageServiceTier'

export type UsageCostIconName = 'creditCard' | 'calendar' | 'questionCircle' | 'bolt' | 'clock' | 'sparkles'

export interface UsageCostVisual {
  kind: string
  icon: UsageCostIconName
  class: string
}

/** Icon + colour for the persisted billing type (0 balance, 1 subscription, otherwise unclassified). */
export function usageBillingVisual(billingType: number | undefined): UsageCostVisual {
  if (billingType === 0) {
    return { kind: 'balance', icon: 'creditCard', class: 'bg-emerald-100 text-emerald-600 dark:bg-emerald-500/20 dark:text-emerald-400' }
  }
  if (billingType === 1) {
    return { kind: 'subscription', icon: 'calendar', class: 'bg-indigo-100 text-indigo-600 dark:bg-indigo-500/20 dark:text-indigo-300' }
  }
  return { kind: 'unclassified', icon: 'questionCircle', class: 'bg-amber-100 text-amber-600 dark:bg-amber-500/20 dark:text-amber-300' }
}

/** Icon for non-default service tiers; Standard/default returns null (nothing rendered). */
export function usageServiceTierVisual(serviceTier?: string | null): UsageCostVisual | null {
  const tier = formatUsageServiceTier(serviceTier)
  if (tier === 'standard') return null
  if (tier === 'priority') {
    return { kind: 'priority', icon: 'bolt', class: 'text-cyan-600 dark:text-cyan-300' }
  }
  if (tier === 'flex') {
    return { kind: 'flex', icon: 'clock', class: 'text-amber-600 dark:text-amber-300' }
  }
  return { kind: tier, icon: 'sparkles', class: 'text-gray-500 dark:text-gray-400' }
}
