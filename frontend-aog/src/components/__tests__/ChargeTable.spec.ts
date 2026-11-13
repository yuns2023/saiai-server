import { describe, expect, it } from 'vitest'
import { mount } from '@vue/test-utils'
import { i18n } from '@/i18n'
import ChargeTable from '../ChargeTable.vue'
import type { UsageLog } from '@/types'

function row(over: Partial<UsageLog>): UsageLog {
  return {
    id: 1, user_id: 1, api_key_id: 1, request_id: 'r', model: 'm', group_id: 1, subscription_id: null,
    input_tokens: 1000, output_tokens: 100, cache_creation_tokens: 0, cache_read_tokens: 0,
    cache_creation_5m_tokens: 0, cache_creation_1h_tokens: 0,
    input_cost: 0.003, output_cost: 0.0015, cache_creation_cost: 0, cache_creation_5m_cost: 0, cache_creation_1h_cost: 0, cache_read_cost: 0,
    total_cost: 0.0045, actual_cost: 0.0036, rate_multiplier: 0.8, billing_type: 0, stream: true,
    duration_ms: 1000, first_token_ms: 200, image_count: 0, image_size: null, user_agent: null,
    cache_ttl_overridden: false, created_at: new Date().toISOString(),
    ...over
  } as UsageLog
}

async function mountOpen(r: UsageLog) {
  i18n.global.locale.value = 'zh'
  const w = mount(ChargeTable, { props: { rows: [r] }, global: { plugins: [i18n] } })
  await w.find('tbody tr').trigger('click')
  return w
}

async function openBreakdown(r: UsageLog): Promise<string> {
  return (await mountOpen(r)).text()
}

/** Text of the breakdown line whose label contains `label`. */
async function line(r: UsageLog, label: string): Promise<string> {
  const w = await mountOpen(r)
  const hit = w.findAll('dl > div').find((d) => d.find('dt').exists() && d.find('dt').text().includes(label))
  return hit ? hit.find('dd').text() : ''
}

describe('ChargeTable breakdown', () => {
  it('itemises token-priced charges as tokens × unit price', async () => {
    const text = await openBreakdown(row({}))
    expect(text).toContain('1,000 × $3.00/M')
    expect(text).toContain('×0.8')
    expect(text).toContain('$0.0036')
  })

  it('shows per-turn charges as one line and unknown tokens as unknown', async () => {
    // Native chat turn as written by the gateway: zero tokens marked unknown,
    // input/output cost reused for the turn and image surcharges, rate applied once.
    const turn = row({
      billing_unit: 'turn', token_usage_source: 'unknown',
      input_tokens: 0, output_tokens: 0, input_cost: 0.02, output_cost: 0.005,
      total_cost: 0.025, actual_cost: 0.02, rate_multiplier: 0.8
    })
    const w = await mountOpen(turn)
    const tokensCell = w.findAll('tbody tr')[0].findAll('td')[3]
    expect(tokensCell.text()).toBe('—')
    expect(tokensCell.attributes('title')).toContain('Tokens')
    expect(w.text()).not.toContain('/M =')
    expect(await line(turn, '按次计费')).toBe('$0.0250')
    expect(await line(turn, '余额扣费')).toBe('$0.0200')
  })

  it('shows per-image charges as images, not tokens', async () => {
    const text = await openBreakdown(row({
      image_count: 2, input_tokens: 0, output_tokens: 0, input_cost: 0, output_cost: 0,
      total_cost: 0.08, actual_cost: 0.064, rate_multiplier: 0.8
    }))
    expect(text).toContain('图片 2 张')
    expect(text).not.toContain('/M =')
    expect(text).toContain('$0.0640')
  })

  it('lists model rate and user discount when they apply', async () => {
    // actual = total × rate × model rate × user discount = 0.0045 × 0.8 × 1.5 × 0.9
    const r = row({ rate_multiplier: 0.8, model_rate_multiplier: 1.5, user_payg_discount_multiplier: 0.9, actual_cost: 0.00486 })
    expect(await line(r, '模型倍率')).toBe('×1.5')
    expect(await line(r, '用户折扣')).toBe('×0.9')
    expect(await line(r, '余额扣费')).toBe('$0.00486')
  })

  it('shows metered amount for hosted rows instead of implying a balance charge', async () => {
    // Hosted policy: zero rate, nothing charged, metered = standard total.
    const r = row({ rate_multiplier: 0, actual_cost: 0, metered_cost: 0.0045 })
    expect(await line(r, '计量金额')).toBe('$0.0045')
    expect(await line(r, '余额扣费')).toBe('$0')
  })

  it('labels subscription charges as subscription usage', async () => {
    expect(await line(row({ billing_type: 1 }), '订阅用量')).toBe('$0.0036')
  })
})
