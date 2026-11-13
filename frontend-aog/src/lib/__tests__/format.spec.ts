import { describe, expect, it } from 'vitest'
import { cost, money, unitPrice } from '../format'

describe('format', () => {
  it('unit prices keep 2–4 decimals without trailing zeros', () => {
    expect(unitPrice(0.3)).toBe('$0.30')
    expect(unitPrice(3)).toBe('$3.00')
    expect(unitPrice(0.305)).toBe('$0.305')
    expect(unitPrice(3.75)).toBe('$3.75')
    expect(unitPrice(0.0375)).toBe('$0.0375')
  })

  it('costs keep enough digits for sub-cent charges', () => {
    expect(cost(0)).toBe('$0')
    expect(cost(0.09632)).toBe('$0.0963')
    expect(cost(0.0000421)).toBe('$0.000042')
    expect(cost(12.345)).toBe('$12.35')
    expect(cost(0.0045)).toBe('$0.0045')
    expect(cost(0.00486)).toBe('$0.00486')
  })

  it('money handles negatives and missing values', () => {
    expect(money(-1.5)).toBe('-$1.50')
    expect(money(null)).toBe('—')
  })
})
