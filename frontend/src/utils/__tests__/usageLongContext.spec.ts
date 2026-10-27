import { describe, expect, it } from 'vitest'
import { usageInputContext, usageLongContext } from '../usageLongContext'
import type { AdminUsageLog, LongContextPricing } from '@/types'

const row = { model: 'gpt-6-astra', input_tokens: 1058, cache_read_tokens: 341248, cache_creation_tokens: 0, service_tier: 'default' }
const info: LongContextPricing = { applied: true, mode: 'whole_request', threshold: 272000, total_input_tokens: 342306,
  input_multiplier: 2, output_multiplier: 1.5, cache_read_multiplier: 2, cache_write_multiplier: 2 }
const snapshot = { version: 'fixture', billed_model: 'custom', resolved_model: 'gpt-6-astra', source: 'remote', reference_total_cost: 1, long_context: info }

describe('stored long-context evidence', () => {
  it('uses explicit stored evidence for aliases and honors an explicit non-applied rule', () => {
    expect(usageLongContext({ ...row, model: 'custom', service_tier: 'priority', pricing_snapshot: snapshot })).toEqual(info)
    expect(usageLongContext({ ...row, pricing_snapshot: { ...snapshot, long_context: { ...info, applied: false } } })).toBeNull()
  })
  it('includes cache writes exactly once and uses the strict greater-than threshold', () => {
    expect(usageLongContext({ ...row, input_tokens: 1000, cache_read_tokens: 270000, cache_creation_tokens: 1000 })).toBeNull()
    expect(usageLongContext({ ...row, input_tokens: 1000, cache_read_tokens: 270000, cache_creation_tokens: 1001, cache_creation_5m_tokens: 1001 })).toMatchObject({ total_input_tokens: 272001, inferred: true })
  })
  it.each(['gpt-5.1', 'gpt-4.1', 'claude-opus-5-5', 'custom-model'])('does not invent a rule for %s', model => {
    expect(usageLongContext({ ...row, model })).toBeNull()
  })
  it('excludes priority, native Chat, and images from historical inference', () => {
    expect(usageLongContext({ ...row, service_tier: 'priority' })).toBeNull()
    expect(usageLongContext({ ...row, billing_unit: 'turn' })).toBeNull()
    expect(usageLongContext({ ...row, image_count: 1 })).toBeNull()
    expect(usageLongContext({ ...row, inbound_endpoint: '/v1/images/generations' })).toBeNull()
  })
  it('derives early snapshots from their stored price policy and preserves split-only rules', () => {
    const early: AdminUsageLog['pricing_snapshot'] = { ...snapshot, long_context: undefined, effective: {
      long_context_threshold: 100000, long_context_input_multiplier: 3, long_context_output_multiplier: 2 } }
    expect(usageLongContext({ ...row, model: 'custom', pricing_snapshot: early })).toMatchObject({ threshold: 100000, input_multiplier: 3 })
    expect(usageLongContext({ ...row, pricing_snapshot: { ...early, effective: undefined } })).toBeNull()
    expect(usageLongContext({ ...row, pricing_snapshot: { ...early, long_context_threshold: 200000, long_context_extra_multiplier: 2 } })).toMatchObject({ mode: 'excess_input', output_multiplier: 1 })
  })
})

describe('input context size against the model long-context tier', () => {
  const total = (n: number, threshold = 272000) => ({ total: n, threshold })
  it.each(['default', 'priority', 'fast', 'flex'])('includes cache for %s even without a surcharge', service_tier => {
    const cached = { ...row, service_tier, pricing_snapshot: { ...snapshot, long_context: { ...info, applied: false } } }
    expect(usageInputContext(cached)).toEqual(total(342306))
    expect(usageLongContext(cached)).toBeNull()
  })
  it('counts cache writes once, excludes output, and uses a strict boundary', () => {
    const boundary = { ...row, input_tokens: 1000, cache_read_tokens: 270000, cache_creation_tokens: 1000, cache_creation_5m_tokens: 1000, output_tokens: 100000 }
    expect(usageInputContext(boundary)).toBeNull()
    expect(usageInputContext({ ...boundary, input_tokens: 1001 })).toEqual(total(272001))
    expect(usageInputContext({ ...row, input_tokens: 317880, cache_read_tokens: 0, output_tokens: 405 })).toEqual(total(317880))
  })
  it('hides the marker for models without a long-context tier', () => {
    const claude = { ...row, model: 'claude-opus-5-5', pricing_snapshot: { ...snapshot, long_context: { ...info, applied: false, threshold: 0 } } }
    expect(usageInputContext(claude)).toBeNull()
    expect(usageInputContext({ ...claude, pricing_snapshot: { ...snapshot, long_context: undefined } })).toBeNull()
    expect(usageInputContext({ ...row, model: 'claude-opus-5-5' })).toBeNull()
    expect(usageInputContext({ ...row, model: 'custom' })).toBeNull()
    expect(usageLongContext({ ...row, model: 'custom' })).toBeNull()
  })
  it('uses the model policy threshold in evidence order', () => {
    const early: AdminUsageLog['pricing_snapshot'] = { ...snapshot, long_context: undefined, effective: { long_context_threshold: 100000, long_context_input_multiplier: 2 } }
    expect(usageInputContext({ ...row, model: 'custom', pricing_snapshot: early })).toEqual(total(342306, 100000))
    expect(usageInputContext({ ...row, model: 'custom', pricing_snapshot: { ...early, long_context_threshold: 200000, long_context_extra_multiplier: 2 } })).toEqual(total(342306, 200000))
    expect(usageInputContext({ ...row, pricing_snapshot: { ...early, long_context: { ...info, threshold: 0 } } })).toBeNull()
  })
  it('requires a multiplier above 1 for a tier', () => {
    expect(usageInputContext({ ...row, pricing_snapshot: { ...snapshot, long_context: { ...info, applied: false, input_multiplier: 1, output_multiplier: 1, cache_read_multiplier: 1, cache_write_multiplier: 1 } } })).toBeNull()
    expect(usageInputContext({ ...row, pricing_snapshot: { ...snapshot, long_context: undefined, effective: { long_context_threshold: 100000 } } })).toBeNull()
  })
  it('excludes non-token usage', () => {
    expect(usageInputContext({ ...row, billing_unit: 'turn' })).toBeNull()
    expect(usageInputContext({ ...row, image_count: 1 })).toBeNull()
    expect(usageInputContext({ ...row, inbound_endpoint: '/v1/images/edits' })).toBeNull()
    expect(usageInputContext(null)).toBeNull()
  })
})
