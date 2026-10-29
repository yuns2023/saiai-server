import { describe, expect, it } from 'vitest'
import type { UsageAnalyticsItem, UsageAnalyticsMetrics } from '@/api/usage'
import {
  analyticsCurlExample,
  analyticsQueryString,
  analyticsShare,
  buildUsageAnalyticsCsv,
  costShareField,
  dedupeAnalyticsRows,
  formatAnalyticsShare,
  orderEffortRows,
  shareBarWidth
} from '@/utils/usageAnalytics'

const metrics = (overrides: Partial<UsageAnalyticsMetrics> = {}): UsageAnalyticsMetrics => ({
  requests: 0,
  input_tokens: 0,
  output_tokens: 0,
  cache_creation_tokens: 0,
  cache_read_tokens: 0,
  total_tokens: 0,
  cost: 0,
  actual_cost: 0,
  usage_cost: 0,
  ...overrides
})

const item = (overrides: Partial<UsageAnalyticsItem> = {}): UsageAnalyticsItem => ({
  ...metrics(),
  share: { requests: 0.5, tokens: 0.25, cost: 0.1, actual_cost: 0.2, usage_cost: 0.3 },
  token_mix: { input: null, output: null, cache_creation: null, cache_read: null },
  ...overrides
})

describe('usage analytics shares', () => {
  it('uses the billed share unless only hosted reference usage exists', () => {
    expect(costShareField(metrics({ actual_cost: 1, usage_cost: 3 }))).toBe('actual_cost')
    expect(costShareField(metrics({ actual_cost: 0, usage_cost: 3 }))).toBe('usage_cost')
    expect(costShareField(metrics())).toBe('actual_cost')
  })

  it('picks the share for the selected metric', () => {
    const billed = metrics({ actual_cost: 1, usage_cost: 1 })
    const hosted = metrics({ actual_cost: 0, usage_cost: 1 })
    expect(analyticsShare(item(), billed, 'cost')).toBe(0.2)
    expect(analyticsShare(item(), hosted, 'cost')).toBe(0.3)
    expect(analyticsShare(item(), billed, 'tokens')).toBe(0.25)
    expect(analyticsShare(item(), billed, 'requests')).toBe(0.5)
    expect(analyticsShare(item({ share: { requests: null, tokens: null, cost: null, actual_cost: null, usage_cost: null } }), billed, 'cost')).toBeNull()
  })

  it('formats shares with two decimals and null as a dash', () => {
    expect(formatAnalyticsShare(0.123456)).toBe('12.35%')
    expect(formatAnalyticsShare(1)).toBe('100.00%')
    expect(formatAnalyticsShare(null)).toBe('—')
    expect(formatAnalyticsShare(Number.NaN)).toBe('—')
    expect(shareBarWidth(null)).toBe('0%')
    expect(shareBarWidth(1.2)).toBe('100%')
  })

  it('lists the not-recorded effort bucket last', () => {
    const rows = [
      { reasoning_effort_missing: true, id: 'missing' },
      { id: 'high' },
      { id: 'none' }
    ]
    expect(orderEffortRows(rows).map((row) => row.id)).toEqual(['high', 'none', 'missing'])
  })
})

describe('usage analytics row de-duplication', () => {
  it('keys rows by their dimension, keeping missing effort distinct from "none"', () => {
    const rows = dedupeAnalyticsRows([
      item({ api_key_id: 1, model: 'a', requests: 1 }),
      item({ api_key_id: 1, model: 'a', requests: 2 }),
      item({ api_key_id: 1, model: 'b' }),
      item({ api_key_id: 2, model: 'a' }),
      item({ reasoning_effort: 'none' }),
      item({ reasoning_effort_missing: true }),
      item({ reasoning_effort_missing: true })
    ])
    expect(rows).toHaveLength(5)
    expect(rows[0].requests).toBe(1)
  })
})

describe('usage analytics API example', () => {
  it('drops empty params and keeps the URL shell-safe', () => {
    expect(analyticsQueryString({ group_by: 'model', model: undefined, page: 1 })).toBe('group_by=model&page=1')
    const curl = analyticsCurlExample('https://example.test/api/v1/usage/analytics', {
      start_date: '2026-10-01',
      timezone: 'America/New_York',
      model: "it's",
      reasoning_effort_missing: true
    })
    expect(curl).toContain(
      "'https://example.test/api/v1/usage/analytics?start_date=2026-10-01&timezone=America%2FNew_York&model=it%27s&reasoning_effort_missing=true'"
    )
    expect(curl).toContain('Authorization: Bearer <YOUR_LOGIN_TOKEN>')
    expect(analyticsCurlExample("https://example.test/o'brien/usage/analytics", {})).toContain(
      "'https://example.test/o%27brien/usage/analytics'"
    )
  })
})

describe('usage analytics CSV', () => {
  it('includes dimension cells, raw numbers, shares, and escapes formulas', () => {
    const csv = buildUsageAnalyticsCsv('api_key_model', [
      item({
        api_key_id: 7,
        api_key_name: '=HYPERLINK("x")',
        api_key_deleted: true,
        model: 'gpt-x',
        requests: 3,
        total_tokens: 10,
        actual_cost: 0.5,
        share: { requests: 1, tokens: 1, cost: null, actual_cost: 1, usage_cost: 1 }
      })
    ])
    const [header, row] = csv.split('\n')
    expect(header.startsWith('"API Key ID","API Key Name","API Key Deleted","Model","Requests"')).toBe(true)
    expect(row).toContain('"7","\'=HYPERLINK(""x"")","true","gpt-x","3"')
    expect(row).toContain('"0.50000000"')
    expect(row).toContain('"100.0000%"')
    // Null standard-cost share stays empty rather than 0%.
    expect(row).toContain('"100.0000%","",')
  })

  it('marks the not-recorded effort bucket explicitly', () => {
    const csv = buildUsageAnalyticsCsv('reasoning_effort', [
      item({ reasoning_effort: 'none' }),
      item({ reasoning_effort_missing: true })
    ])
    const lines = csv.split('\n')
    expect(lines[0].startsWith('"Reasoning Effort","Reasoning Effort Not Recorded"')).toBe(true)
    expect(lines[1].startsWith('"none","false"')).toBe(true)
    expect(lines[2].startsWith('"","true"')).toBe(true)
  })
})
