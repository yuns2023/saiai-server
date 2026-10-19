// Classify by the persisted billing type, even when a deleted subscription has no ID.
export const usageBillingLabel = (billingType: number | undefined): string =>
  billingType === 0 ? 'usage.balanceCharge' : billingType === 1 ? 'usage.subscriptionUsage' : 'usage.unclassifiedUsage'
