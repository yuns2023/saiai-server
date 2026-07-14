import { describe, expect, it } from 'vitest'
import { escapeCsvCell } from '@/utils/csv'

describe('escapeCsvCell', () => {
  it('quotes separators, quotes, and line breaks', () => {
    expect(escapeCsvCell('alpha,beta')).toBe('"alpha,beta"')
    expect(escapeCsvCell('say "hello"')).toBe('"say ""hello"""')
    expect(escapeCsvCell('line 1\nline 2')).toBe('"line 1\nline 2"')
  })

  it('neutralizes spreadsheet formula prefixes', () => {
    expect(escapeCsvCell('=1+1')).toBe('"\'=1+1"')
    expect(escapeCsvCell('+cmd')).toBe('"\'+cmd"')
    expect(escapeCsvCell('-2+3')).toBe('"\'-2+3"')
    expect(escapeCsvCell('@SUM(A1:A2)')).toBe('"\'@SUM(A1:A2)"')
    expect(escapeCsvCell('\t=1+1')).toBe('"\'\t=1+1"')
    expect(escapeCsvCell('\n=1+1')).toBe('"\'\n=1+1"')
    expect(escapeCsvCell('  =1+1')).toBe('"\'  =1+1"')
  })

  it('preserves numbers and empty values', () => {
    expect(escapeCsvCell(42)).toBe('"42"')
    expect(escapeCsvCell(null)).toBe('""')
    expect(escapeCsvCell(undefined)).toBe('""')
  })
})
