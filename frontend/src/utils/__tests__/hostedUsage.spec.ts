import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { describe, expect, it } from 'vitest'
import { hostedFeeBadge, isHostedUsageEligible, supportsHostedUsage } from '../hostedUsage'
import zh from '@/i18n/locales/zh'
import en from '@/i18n/locales/en'

const base = { platform: 'openai', is_exclusive: true, subscription_type: 'standard', rate_multiplier: 0 }

describe('isHostedUsageEligible', () => {
  it('accepts a zero-rate exclusive standard OpenAI or Anthropic group', () => {
    expect(isHostedUsageEligible(base)).toBe(true)
    expect(isHostedUsageEligible({ ...base, rate_multiplier: '0' })).toBe(true)
    expect(isHostedUsageEligible({ ...base, platform: 'anthropic' })).toBe(true)
  })

  it('accepts any finite non-negative rate as the hosting fee', () => {
    expect(isHostedUsageEligible({ ...base, rate_multiplier: 0.1 })).toBe(true)
    expect(isHostedUsageEligible({ ...base, rate_multiplier: '0.1' })).toBe(true)
    expect(isHostedUsageEligible({ ...base, rate_multiplier: 0.001 })).toBe(true)
    expect(isHostedUsageEligible({ ...base, rate_multiplier: 2 })).toBe(true)
  })

  it('offers the hosted option only on supported platforms', () => {
    expect(supportsHostedUsage('openai')).toBe(true)
    expect(supportsHostedUsage('anthropic')).toBe(true)
    expect(supportsHostedUsage('gemini')).toBe(false)
    expect(supportsHostedUsage(undefined)).toBe(false)
  })

  it.each([
    ['negative rate', { rate_multiplier: -0.1 }],
    ['non-numeric rate', { rate_multiplier: 'abc' }],
    ['NaN rate', { rate_multiplier: Number.NaN }],
    ['infinite rate', { rate_multiplier: Number.POSITIVE_INFINITY }],
    ['empty rate input', { rate_multiplier: '' }],
    ['missing rate', { rate_multiplier: null }],
    ['public group', { is_exclusive: false }],
    ['subscription group', { subscription_type: 'subscription' }],
    ['unsupported platform', { platform: 'gemini' }],
  ])('rejects %s', (_, patch) => {
    expect(isHostedUsageEligible({ ...base, ...patch })).toBe(false)
  })
})

describe('hostedFeeBadge', () => {
  // The test build of vue-i18n has no message compiler, so look up and interpolate the real locale strings directly.
  const translate = (locale: 'zh' | 'en') => (key: string, params: Record<string, unknown> = {}) => {
    const message = key.split('.').reduce<any>((node, part) => node?.[part], locale === 'zh' ? zh : en)
    if (typeof message !== 'string') throw new Error(`missing ${locale} message ${key}`)
    return message.replace(/\{(\w+)\}/g, (_, name: string) => String(params[name]))
  }

  it('says not charged for a zero rate and shows the fee for a positive rate', () => {
    const t = translate('zh')
    expect(hostedFeeBadge(t, 0)).toBe('自有账号托管 · 不扣费')
    expect(hostedFeeBadge(t, 0.1)).toBe('自有账号托管 · 服务费 0.1× 标准价')
    expect(translate('en')("hostedUsage.badgeFee", { rate: '0.1' })).toBe('Own-account hosting · fee 0.1× standard price')
    expect(hostedFeeBadge(translate('en'), 0)).toBe('Own-account hosting · not charged')
  })

  it('treats missing or invalid rates as the free wording', () => {
    const t = translate('zh')
    for (const rate of [null, undefined, Number.NaN, -1]) expect(hostedFeeBadge(t, rate)).toBe('自有账号托管 · 不扣费')
  })
})

describe('GroupsView rate multiplier inputs', () => {
  const source = readFileSync(resolve(process.cwd(), 'src/views/admin/GroupsView.vue'), 'utf8')

  it('allow a zero group rate so hosted usage can be enabled', () => {
    const rateInputs = source.match(/v-model\.number="(?:create|edit)Form\.rate_multiplier"[\s\S]*?\/>/g) ?? []
    expect(rateInputs).toHaveLength(2)
    for (const input of rateInputs) {
      expect(input).toContain('min="0"')
      expect(input).not.toContain('min="0.001"')
    }
  })

  it('keeps hosted groups out of fallback choices and clears hosted on unsupported platforms', () => {
    expect(source.match(/!g\.hosted_usage_enabled/g)).toHaveLength(4)
    expect(source.match(/if \(!supportsHostedUsage\(newVal\)\) (?:create|edit)Form\.hosted_usage_enabled = false/g)).toHaveLength(2)
  })

  it('relabels both rate inputs as the hosting fee rate when hosted is on', () => {
    expect(source.match(/(?:create|edit)Form\.hosted_usage_enabled \? t\('hostedUsage\.feeRateLabel'\) : t\('admin\.groups\.form\.rateMultiplier'\)/g)).toHaveLength(2)
    expect(source.match(/hostedUsage\.feeRateHint/g)).toHaveLength(2)
  })

  it('shows the blocked reason next to both hosted checkboxes', () => {
    expect(source.match(/data-testid="hosted-usage-blocked-reason"/g)).toHaveLength(2)
    expect(source.match(/:disabled="!(?:create|edit)Form\.hosted_usage_enabled && !isHostedUsageEligible\((?:create|edit)Form\)"/g)).toHaveLength(2)
  })
})
