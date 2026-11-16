import { describe, expect, it } from 'vitest'
import { compareDecimal, fromUnits, isPositive, normaliseAmount, toUnits, usd } from '../decimal'

describe('toUnits / fromUnits', () => {
  it('parses exactly at 6 decimals', () => {
    expect(toUnits('12.345678')).toBe(BigInt(12345678))
    expect(toUnits('0.1')).toBe(BigInt(100000))
    expect(toUnits('10')).toBe(BigInt(10000000))
    expect(toUnits('-1.5')).toBe(BigInt(-1500000))
    expect(toUnits('1.0000000')).toBe(BigInt(1000000))
  })
  it('rejects junk and excess precision', () => {
    for (const bad of ['', 'abc', '1.2.3', '1e3', '.5', '1.0000001', ' ', null, undefined]) expect(toUnits(bad as string)).toBeNull()
  })
  it('does not lose large values to float', () => {
    expect(fromUnits(toUnits('9007199254740993.000001')!)).toBe('9007199254740993.000001')
  })
  it('trims trailing zeros down to minDigits', () => {
    expect(fromUnits(BigInt(10000000), 2)).toBe('10.00')
    expect(fromUnits(BigInt(12345678), 2)).toBe('12.345678')
    expect(fromUnits(BigInt(5), 0)).toBe('0.000005')
    expect(fromUnits(BigInt(-1500000), 2)).toBe('-1.50')
  })
})

describe('compareDecimal / isPositive', () => {
  it('compares without float error', () => {
    expect(compareDecimal('0.3', '0.1')).toBe(1)
    expect(compareDecimal('10', '10.000000')).toBe(0)
    expect(compareDecimal('9.999999', '10')).toBe(-1)
    expect(compareDecimal('bad', '0')).toBe(0)
  })
  it('isPositive', () => {
    expect(isPositive('0.000001')).toBe(true)
    expect(isPositive('0')).toBe(false)
    expect(isPositive('0.000000')).toBe(false)
    expect(isPositive('-1')).toBe(false)
    expect(isPositive('x')).toBe(false)
  })
})

describe('usd', () => {
  it('formats with at least two decimals', () => {
    expect(usd('10')).toBe('$10.00')
    expect(usd('12.345678')).toBe('$12.345678')
    expect(usd('0.1')).toBe('$0.10')
    expect(usd('-2.5')).toBe('-$2.50')
    expect(usd('nope')).toBe('—')
  })
})

describe('normaliseAmount', () => {
  it('accepts plain amounts within range', () => {
    expect(normaliseAmount('50', '1', '10000')).toBe('50')
    expect(normaliseAmount(' 10.50 ', '1', '10000')).toBe('10.5')
    expect(normaliseAmount('007', '1', '10000')).toBe('7')
    expect(normaliseAmount('1', '1', '10000')).toBe('1')
    expect(normaliseAmount('10000.00', '1', '10000')).toBe('10000')
    expect(normaliseAmount('10.', '1', '10000')).toBe('10')
  })
  it('rejects out of range, too precise or malformed', () => {
    expect(normaliseAmount('0.99', '1', '10000')).toBeNull()
    expect(normaliseAmount('10000.01', '1', '10000')).toBeNull()
    expect(normaliseAmount('1.234', '1', '10000')).toBeNull()
    expect(normaliseAmount('-5', '1', '10000')).toBeNull()
    expect(normaliseAmount('1e2', '1', '10000')).toBeNull()
    expect(normaliseAmount('', '1', '10000')).toBeNull()
  })
})
