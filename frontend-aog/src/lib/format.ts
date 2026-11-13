/** Formatting helpers. Amounts are USD balance units unless a currency is given. */

export function money(value: number | null | undefined, digits = 2): string {
  if (value == null || Number.isNaN(value)) return '—'
  const sign = value < 0 ? '-' : ''
  return `${sign}$${Math.abs(value).toFixed(digits)}`
}

/** Per-request costs are often sub-cent; keep enough digits to stay honest. */
export function cost(value: number | null | undefined): string {
  if (value == null || Number.isNaN(value)) return '—'
  if (value === 0) return '$0'
  const abs = Math.abs(value)
  const digits = abs >= 1 ? 2 : abs >= 0.01 ? 4 : 6
  const minDigits = abs >= 1 ? 2 : 4
  // Up to `digits` places, trailing zeros trimmed back to `minDigits` ($0.004500 → $0.0045).
  const [int, frac = ''] = abs.toFixed(digits).split('.')
  const trimmed = frac.replace(/0+$/, '').padEnd(minDigits, '0')
  return `${value < 0 ? '-' : ''}$${int}.${trimmed}`
}

/** Price per million tokens: at least 2 decimals, up to 4, no trailing zeros beyond 2. */
export function unitPrice(value: number): string {
  if (!Number.isFinite(value)) return '—'
  const [int, frac = ''] = value.toFixed(4).split('.')
  return `$${int}.${frac.replace(/0+$/, '').padEnd(2, '0')}`
}

export function currency(value: number, code: string, locale: string): string {
  try {
    return new Intl.NumberFormat(locale, { style: 'currency', currency: code }).format(value)
  } catch {
    return `${code} ${value.toFixed(2)}`
  }
}

export function count(value: number | null | undefined, locale: string): string {
  if (value == null) return '—'
  return new Intl.NumberFormat(locale).format(value)
}

export function compact(value: number | null | undefined, locale: string): string {
  if (value == null) return '—'
  return new Intl.NumberFormat(locale, { notation: 'compact', maximumFractionDigits: 1 }).format(value)
}

export function rate(value: number | null | undefined): string {
  if (value == null) return '—'
  return `×${Number(value.toFixed(4))}`
}

export function dateTime(iso: string | null | undefined, locale: string): string {
  if (!iso) return '—'
  return new Intl.DateTimeFormat(locale, {
    year: 'numeric', month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit', second: '2-digit', hour12: false
  }).format(new Date(iso))
}

/** Statement time: "HH:mm" today, "MM/DD HH:mm" otherwise (full time in the title). */
export function shortTime(iso: string | null | undefined, locale: string): string {
  if (!iso) return '—'
  const d = new Date(iso)
  const sameDay = d.toDateString() === new Date().toDateString()
  return new Intl.DateTimeFormat(locale, sameDay
    ? { hour: '2-digit', minute: '2-digit', hour12: false }
    : { month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit', hour12: false }
  ).format(d)
}

export function dateOnly(iso: string | null | undefined, locale: string): string {
  if (!iso) return '—'
  return new Intl.DateTimeFormat(locale, { year: 'numeric', month: '2-digit', day: '2-digit' }).format(new Date(iso))
}

/** Local calendar date as YYYY-MM-DD (the format the usage API expects). */
export function ymd(d: Date): string {
  const y = d.getFullYear()
  const m = String(d.getMonth() + 1).padStart(2, '0')
  const day = String(d.getDate()).padStart(2, '0')
  return `${y}-${m}-${day}`
}

export function daysAgo(n: number, from = new Date()): Date {
  const d = new Date(from)
  d.setDate(d.getDate() - n)
  return d
}

export function maskKey(key: string): string {
  if (key.length <= 12) return key
  return `${key.slice(0, 7)}…${key.slice(-4)}`
}

export function timezone(): string {
  try {
    return Intl.DateTimeFormat().resolvedOptions().timeZone
  } catch {
    return 'UTC'
  }
}
