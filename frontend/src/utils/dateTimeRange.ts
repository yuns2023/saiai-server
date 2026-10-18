export interface DateTimeRange {
  startTime: string
  endTime: string
  preset: string | null
}

export function localDateTime(date: Date): string {
  const pad = (n: number) => String(n).padStart(2, '0')
  return `${date.getFullYear()}-${pad(date.getMonth() + 1)}-${pad(date.getDate())}T${pad(date.getHours())}:${pad(date.getMinutes())}`
}

// All ranges are half-open: start inclusive, end exclusive.
export function presetTimeRange(preset: string, now = new Date()): DateTimeRange {
  const end = new Date(now)
  end.setSeconds(0, 0)
  const start = new Date(end)
  if (preset === 'last24Hours') {
    start.setTime(end.getTime() - 86400000)
  } else {
    start.setHours(0, 0, 0, 0)
    if (preset === 'yesterday') {
      end.setHours(0, 0, 0, 0)
      start.setDate(start.getDate() - 1)
    } else if (preset === 'lastMonth') {
      end.setDate(1)
      end.setHours(0, 0, 0, 0)
      start.setDate(1)
      start.setMonth(start.getMonth() - 1)
    } else if (preset === 'thisMonth') {
      start.setDate(1)
    } else if (/^\d+days$/.test(preset)) {
      start.setDate(start.getDate() - parseInt(preset) + 1)
    }
  }
  return { startTime: start.toISOString(), endTime: end.toISOString(), preset }
}
