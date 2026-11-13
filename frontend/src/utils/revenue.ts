import type { RevenueParts } from '@/types'

// Totals of a ranking/summary response, in the shape RevenueAmount expects.
export interface RevenueTotals {
  total_subscription_revenue?: number | null
  total_subscription_revenue_unpriced_cost?: number | null
  total_balance_cost?: number | null
  total_subscription_cost?: number | null
  total_unclassified_cost?: number | null
}

export function revenuePartsFromTotals(totals: RevenueTotals): RevenueParts {
  return {
    subscription_revenue: totals.total_subscription_revenue ?? 0,
    subscription_revenue_unpriced_cost: totals.total_subscription_revenue_unpriced_cost ?? 0,
    balance_cost: totals.total_balance_cost ?? 0,
    subscription_cost: totals.total_subscription_cost ?? 0,
    unclassified_cost: totals.total_unclassified_cost ?? 0,
  }
}
