export interface HostedUsageEligibilityForm {
  platform?: string
  is_exclusive: boolean
  subscription_type: string
  rate_multiplier: number | string | null | undefined
}

// Mirrors backend hostedUsagePlatform.
export function supportsHostedUsage(platform?: string): boolean {
  return platform === 'openai' || platform === 'anthropic'
}

// Mirrors backend Group.UsesHostedUsage: hosted metering needs an exclusive
// standard OpenAI or Anthropic group with a finite, non-negative group rate.
// That rate is the hosting service fee (× standard price); 0 means free hosting.
export function isHostedUsageEligible(form: HostedUsageEligibilityForm): boolean {
  const rate = form.rate_multiplier
  return (form.platform === undefined || supportsHostedUsage(form.platform)) &&
    form.is_exclusive &&
    form.subscription_type === 'standard' &&
    rate !== '' && rate !== null && rate !== undefined &&
    Number.isFinite(Number(rate)) && Number(rate) >= 0
}

type Translate = (key: string, params?: Record<string, unknown>) => string

// Hosted billing wording. Hosted groups charge standard price × the group rate
// (never a user override); a zero rate is the free hosting of old.
export function hostedFeeBadge(t: Translate, rate: number | null | undefined): string {
  const value = Number(rate)
  return rate != null && Number.isFinite(value) && value > 0
    ? t('hostedUsage.badgeFee', { rate: String(value) })
    : t('hostedUsage.badgeFree')
}
