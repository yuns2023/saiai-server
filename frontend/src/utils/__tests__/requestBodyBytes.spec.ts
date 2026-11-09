import { describe, expect, it } from 'vitest'
import { formatRequestBodyBytes, requestBodyBytesCsvValue } from '../requestBodyBytes'

describe('formatRequestBodyBytes', () => {
  it('shows "-" only for unknown sizes', () => {
    expect(formatRequestBodyBytes(null)).toBe('-')
    expect(formatRequestBodyBytes(undefined)).toBe('-')
    expect(formatRequestBodyBytes(0)).toBe('0 B')
  })

  it('uses binary units', () => {
    expect(formatRequestBodyBytes(512)).toBe('512 B')
    expect(formatRequestBodyBytes(1023)).toBe('1023 B')
    expect(formatRequestBodyBytes(1024)).toBe('1.0 KiB')
    expect(formatRequestBodyBytes(1536)).toBe('1.5 KiB')
    expect(formatRequestBodyBytes(48 * 1024 * 1024)).toBe('48.0 MiB')
    expect(formatRequestBodyBytes(300 * 1024 * 1024)).toBe('300 MiB')
    expect(formatRequestBodyBytes(3 * 1024 ** 3)).toBe('3.0 GiB')
  })
})

describe('requestBodyBytesCsvValue', () => {
  it('exports exact integer bytes, empty for unknown, keeps 0', () => {
    expect(requestBodyBytesCsvValue(null)).toBe('')
    expect(requestBodyBytesCsvValue(undefined)).toBe('')
    expect(requestBodyBytesCsvValue(0)).toBe('0')
    expect(requestBodyBytesCsvValue(50331648)).toBe('50331648')
  })
})
