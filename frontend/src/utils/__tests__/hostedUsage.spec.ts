import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { describe, expect, it } from 'vitest'
import { isHostedUsageEligible } from '../hostedUsage'

const base = { platform: 'openai', is_exclusive: true, subscription_type: 'standard', rate_multiplier: 0 }

describe('isHostedUsageEligible', () => {
  it('accepts a zero-rate exclusive standard OpenAI group', () => {
    expect(isHostedUsageEligible(base)).toBe(true)
    expect(isHostedUsageEligible({ ...base, rate_multiplier: '0' })).toBe(true)
  })

  it.each([
    ['nonzero rate', { rate_multiplier: 0.001 }],
    ['empty rate input', { rate_multiplier: '' }],
    ['missing rate', { rate_multiplier: null }],
    ['public group', { is_exclusive: false }],
    ['subscription group', { subscription_type: 'subscription' }],
    ['non-OpenAI platform', { platform: 'anthropic' }],
  ])('rejects %s', (_, patch) => {
    expect(isHostedUsageEligible({ ...base, ...patch })).toBe(false)
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

  it('shows the blocked reason next to both hosted checkboxes', () => {
    expect(source.match(/data-testid="hosted-usage-blocked-reason"/g)).toHaveLength(2)
    expect(source.match(/:disabled="!(?:create|edit)Form\.hosted_usage_enabled && !isHostedUsageEligible\((?:create|edit)Form\)"/g)).toHaveLength(2)
  })
})
