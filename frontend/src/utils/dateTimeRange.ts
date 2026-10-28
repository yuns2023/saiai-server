export interface DateTimeRange {
  startTime: string
  endTime: string
  preset: string | null
}

export function localDateTime(date: Date): string {
  const pad = (n: number) => String(n).padStart(2, '0')
  return `${date.getFullYear()}-${pad(date.getMonth() + 1)}-${pad(date.getDate())}T${pad(date.getHours())}:${pad(date.getMinutes())}`
}

// All ranges are half-open: start inclusive, end exclusive. Presets that
// include today run to the next local midnight (whole calendar days), so they
// are never empty right after midnight and rows written after the range was
// picked still match. The rolling last 24 hours ends at the next whole minute,
// which covers the current one.
export function presetTimeRange(preset: string, now = new Date()): DateTimeRange {
  // Floor in epoch time: a local setSeconds() is ambiguous in the repeated DST hour.
  const end = new Date(Math.floor(now.getTime() / 60000) * 60000)
  const start = new Date(end)
  if (preset === 'last24Hours') {
    end.setTime(end.getTime() + 60000)
    start.setTime(end.getTime() - 86400000)
  } else {
    start.setHours(0, 0, 0, 0)
    end.setTime(start.getTime())
    if (preset === 'yesterday') {
      start.setDate(start.getDate() - 1)
    } else if (preset === 'lastMonth') {
      end.setDate(1)
      start.setDate(1)
      start.setMonth(start.getMonth() - 1)
    } else {
      end.setDate(end.getDate() + 1)
      if (preset === 'thisMonth') {
        start.setDate(1)
      } else if (/^\d+days$/.test(preset)) {
        start.setDate(start.getDate() - parseInt(preset) + 1)
      }
    }
  }
  return { startTime: start.toISOString(), endTime: end.toISOString(), preset }
}
