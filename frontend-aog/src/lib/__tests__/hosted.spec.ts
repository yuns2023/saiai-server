import { beforeAll, describe, expect, it } from 'vitest'
import { localMidnight, rangeProblem, rfc3339, utcWindow } from '../hosted'

/** Runs the block with the process time zone switched (Node re-reads TZ on assignment). */
function inZone(zone: string, fn: () => void): () => void {
  return () => {
    const saved = process.env.TZ
    process.env.TZ = zone
    try {
      fn()
    } finally {
      if (saved === undefined) delete process.env.TZ
      else process.env.TZ = saved
    }
  }
}

describe('hosted date range', () => {
  let zoneWorks = false
  beforeAll(inZone('Asia/Shanghai', () => {
    zoneWorks = new Date(2026, 10, 10).getTimezoneOffset() === -480
  }))

  it('time zone switching is available in this runner', () => {
    expect(zoneWorks).toBe(true)
  })

  it('rejects malformed and impossible dates', () => {
    expect(localMidnight('2026-02-30')).toBeNull()
    expect(localMidnight('2026/11/10')).toBeNull()
    expect(localMidnight('')).toBeNull()
    expect(rangeProblem('', '2026-11-10')).toBe('invalid')
  })

  it('formats RFC3339 without fractional seconds', () => {
    expect(rfc3339(new Date(Date.UTC(2026, 10, 9, 16, 0, 0, 0)))).toBe('2026-11-09T16:00:00Z')
  })

  it(
    'turns local days into a half-open UTC window (UTC+8)',
    inZone('Asia/Shanghai', () => {
      expect(utcWindow('2026-11-10', '2026-11-16')).toEqual({ start: '2026-11-09T16:00:00Z', end: '2026-11-16T16:00:00Z' })
      // A single day is one whole local day.
      expect(utcWindow('2026-11-16', '2026-11-16')).toEqual({ start: '2026-11-15T16:00:00Z', end: '2026-11-16T16:00:00Z' })
    })
  )

  it(
    'keeps local midnights across a DST change (America/New_York, 2026-11-01)',
    inZone('America/New_York', () => {
      expect(utcWindow('2026-10-31', '2026-11-01')).toEqual({ start: '2026-10-31T04:00:00Z', end: '2026-11-02T05:00:00Z' })
    })
  )

  it(
    'allows at most 31 days and never an inverted range',
    inZone('Asia/Shanghai', () => {
      expect(rangeProblem('2026-10-17', '2026-11-16')).toBeNull() // 31 days
      expect(rangeProblem('2026-10-16', '2026-11-16')).toBe('tooLong') // 32 days
      expect(utcWindow('2026-10-16', '2026-11-16')).toBeNull()
      expect(rangeProblem('2026-11-16', '2026-11-15')).toBe('inverted')
    })
  )

  it(
    'rejects 31 calendar days that are 31 days + 1 hour long (DST fall-back)',
    inZone('America/New_York', () => {
      // 2026-10-15..2026-11-14 spans the 2026-11-01 fall-back: 31 days and one hour.
      expect(rangeProblem('2026-10-15', '2026-11-14')).toBe('tooLong')
      expect(rangeProblem('2026-10-16', '2026-11-14')).toBeNull()
    })
  )
})
