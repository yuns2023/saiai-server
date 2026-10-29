import type {
  UsageAnalyticsGroupBy,
  UsageAnalyticsItem,
  UsageAnalyticsMetrics,
  UsageAnalyticsParams
} from '@/api/usage'
import { escapeCsvCell } from '@/utils/csv'

export type UsageAnalyticsMetric = 'cost' | 'tokens' | 'requests'

/**
 * Cost shares follow the billed amount. Hosted-only results bill nothing
 * (actual 0) but carry a metered reference usage, so fall back to it there.
 */
export function costShareField(summary: UsageAnalyticsMetrics): 'actual_cost' | 'usage_cost' {
  return summary.actual_cost <= 0 && summary.usage_cost > 0 ? 'usage_cost' : 'actual_cost'
}

export function analyticsShare(
  item: Pick<UsageAnalyticsItem, 'share'>,
  summary: UsageAnalyticsMetrics,
  metric: UsageAnalyticsMetric
): number | null {
  const share =
    metric === 'tokens'
      ? item.share.tokens
      : metric === 'requests'
        ? item.share.requests
        : item.share[costShareField(summary)]
  return share ?? null
}

/** Fraction (0..1) → percentage with two decimals; null/invalid → "—". */
export function formatAnalyticsShare(value: number | null | undefined): string {
  return value == null || !Number.isFinite(value) ? '—' : `${(value * 100).toFixed(2)}%`
}

/** Bar width for a fraction, clamped to 0..100%. */
export function shareBarWidth(value: number | null | undefined): string {
  if (value == null || !Number.isFinite(value)) return '0%'
  return `${Math.min(100, Math.max(0, value * 100))}%`
}

/** Keep server order but list the "not recorded" effort bucket last. */
export function orderEffortRows<T extends Pick<UsageAnalyticsItem, 'reasoning_effort_missing'>>(
  items: T[]
): T[] {
  return [
    ...items.filter((item) => !item.reasoning_effort_missing),
    ...items.filter((item) => item.reasoning_effort_missing)
  ]
}

/** Stable row key across all dimensions. */
export function analyticsRowKey(item: UsageAnalyticsItem): string {
  return [
    item.api_key_id ?? '',
    item.model ?? '',
    item.reasoning_effort_missing ? '\u0000missing' : (item.reasoning_effort ?? '')
  ].join('\u0001')
}

/** Keep the first row per dimension key (rows can repeat across pages when rankings shift). */
export function dedupeAnalyticsRows(items: UsageAnalyticsItem[]): UsageAnalyticsItem[] {
  const seen = new Set<string>()
  return items.filter((item) => {
    const key = analyticsRowKey(item)
    if (seen.has(key)) return false
    seen.add(key)
    return true
  })
}

/** Query string of the defined params, in insertion order. */
export function analyticsQueryString(params: UsageAnalyticsParams): string {
  const search = new URLSearchParams()
  for (const [key, value] of Object.entries(params)) {
    if (value === undefined || value === null || value === '') continue
    search.append(key, String(value))
  }
  return search.toString()
}

/**
 * Example request with a placeholder token only. Apostrophes are percent-encoded
 * (URLSearchParams already does so for the query), keeping the single-quoted URL
 * shell-safe even if the configured endpoint path contains one.
 */
export function analyticsCurlExample(endpointURL: string, params: UsageAnalyticsParams): string {
  const query = analyticsQueryString(params)
  const url = (query ? `${endpointURL}?${query}` : endpointURL).replace(/'/g, '%27')
  return `curl -sS '${url}' \\\n  -H 'Authorization: Bearer <YOUR_LOGIN_TOKEN>'`
}

const formatCsvCost = (value: number) => value.toFixed(8)
const formatCsvShare = (value: number | null | undefined) =>
  value == null || !Number.isFinite(value) ? '' : `${(value * 100).toFixed(4)}%`

/** Full CSV text (raw numbers + shares) for one dimension; cells are formula-escaped. */
export function buildUsageAnalyticsCsv(
  groupBy: UsageAnalyticsGroupBy,
  rows: UsageAnalyticsItem[]
): string {
  const keyHeaders = ['API Key ID', 'API Key Name', 'API Key Deleted']
  const dimensionHeaders: Record<UsageAnalyticsGroupBy, string[]> = {
    model: ['Model'],
    reasoning_effort: ['Reasoning Effort', 'Reasoning Effort Not Recorded'],
    api_key: keyHeaders,
    api_key_model: [...keyHeaders, 'Model']
  }
  const dimensionCells = (item: UsageAnalyticsItem): unknown[] => {
    const key = [item.api_key_id ?? '', item.api_key_name ?? '', item.api_key_deleted ? 'true' : 'false']
    switch (groupBy) {
      case 'model':
        return [item.model ?? '']
      case 'reasoning_effort':
        return [item.reasoning_effort ?? '', item.reasoning_effort_missing ? 'true' : 'false']
      case 'api_key':
        return key
      case 'api_key_model':
        return [...key, item.model ?? '']
    }
  }
  const headers = [
    ...dimensionHeaders[groupBy],
    'Requests',
    'Input Tokens',
    'Output Tokens',
    'Cache Creation Tokens',
    'Cache Read Tokens',
    'Total Tokens',
    'Actual Cost',
    'Standard Cost',
    'Usage Cost',
    'Requests Share',
    'Tokens Share',
    'Actual Cost Share',
    'Standard Cost Share',
    'Usage Cost Share'
  ]
  const lines = rows.map((item) =>
    [
      ...dimensionCells(item),
      item.requests,
      item.input_tokens,
      item.output_tokens,
      item.cache_creation_tokens,
      item.cache_read_tokens,
      item.total_tokens,
      formatCsvCost(item.actual_cost),
      formatCsvCost(item.cost),
      formatCsvCost(item.usage_cost),
      formatCsvShare(item.share.requests),
      formatCsvShare(item.share.tokens),
      formatCsvShare(item.share.actual_cost),
      formatCsvShare(item.share.cost),
      formatCsvShare(item.share.usage_cost)
    ]
      .map(escapeCsvCell)
      .join(',')
  )
  return [headers.map(escapeCsvCell).join(','), ...lines].join('\n')
}
