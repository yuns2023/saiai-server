// Inbound request payload size of a usage record (admin only). null/undefined
// means unknown (older records or paths without a trustworthy size); 0 is a
// genuinely empty body and must stay distinguishable from unknown.

const UNITS = ['KiB', 'MiB', 'GiB', 'TiB']

/** Human-readable binary size for tables, e.g. "512 B", "1.5 KiB", "48.0 MiB"; "-" when unknown. */
export function formatRequestBodyBytes(value: number | null | undefined): string {
  if (value == null || !Number.isFinite(value) || value < 0) return '-'
  if (value < 1024) return `${Math.round(value)} B`
  let size = value
  let unit = -1
  while (size >= 1024 && unit < UNITS.length - 1) {
    size /= 1024
    unit++
  }
  const digits = size >= 100 ? 0 : 1
  return `${size.toFixed(digits)} ${UNITS[unit]}`
}

/** Exact integer bytes for CSV export: empty for unknown, "0" for an empty body. */
export function requestBodyBytesCsvValue(value: number | null | undefined): string {
  if (value == null || !Number.isFinite(value)) return ''
  return String(Math.trunc(value))
}

interface RequestBodySizeAggregate {
  request_body_bytes_avg?: number | null
  request_body_bytes_max?: number | null
  request_body_bytes_known_count?: number
}

/** Values for the ranking "avg / max / count" detail line. */
export function requestBodySizeDetailParams(item: RequestBodySizeAggregate) {
  return {
    avg: formatRequestBodyBytes(item.request_body_bytes_avg),
    max: formatRequestBodyBytes(item.request_body_bytes_max),
    count: (item.request_body_bytes_known_count ?? 0).toLocaleString()
  }
}
