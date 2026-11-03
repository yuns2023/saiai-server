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
// standard OpenAI or Anthropic group whose group rate multiplier is exactly 0.
export function isHostedUsageEligible(form: HostedUsageEligibilityForm): boolean {
  const rate = form.rate_multiplier
  return (form.platform === undefined || supportsHostedUsage(form.platform)) &&
    form.is_exclusive &&
    form.subscription_type === 'standard' &&
    rate !== '' && rate !== null && rate !== undefined &&
    Number(rate) === 0
}
